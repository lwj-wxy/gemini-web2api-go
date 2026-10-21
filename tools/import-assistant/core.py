"""Credential parsing, local TOTP, and encrypted durable queue. No browser IO."""
import base64
from contextlib import contextmanager
import csv
import hashlib
import hmac
import io
import json
import os
from pathlib import Path
import re
import sqlite3
import struct
import time
from urllib.parse import parse_qs, urlparse
import uuid

from cryptography.fernet import Fernet

TERMINAL = ('imported', 'already_present', 'failed', 'cancelled', 'interrupted', 'expired')
PUBLIC_COLUMNS = 'id,email,state,message,proxy_id,proxy_name,account_id,created_at,updated_at'


def normalize_seed(value):
    if value.startswith('otpauth://'):
        parsed = urlparse(value)
        parameters = parse_qs(parsed.query)
        if parsed.netloc != 'totp' or parameters.get('algorithm', ['SHA1'])[0].upper() != 'SHA1' or parameters.get('digits', ['6'])[0] != '6' or parameters.get('period', ['30'])[0] != '30':
            raise ValueError('2FA 须使用 SHA1、6 位、30 秒的 TOTP 密钥')
        value = parameters.get('secret', [''])[0]
    value = re.sub(r'\s+', '', value).upper().rstrip('=')
    if not re.fullmatch('[A-Z2-7]{16,208}', value):
        raise ValueError('2FA 字段须填写密钥，而非六位临时验证码')
    try:
        decoded = base64.b32decode(value + '=' * (-len(value) % 8))
    except Exception:
        raise ValueError('2FA 密钥格式不正确') from None
    if not 10 <= len(decoded) <= 128:
        raise ValueError('2FA 密钥长度不正确')
    return value


def totp(secret, timestamp=None, digits=6):
    secret = normalize_seed(secret)
    key = base64.b32decode(secret + '=' * (-len(secret) % 8))
    step = int(time.time() if timestamp is None else timestamp) // 30
    digest = hmac.new(key, struct.pack('>Q', step), hashlib.sha1).digest()
    offset = digest[-1] & 15
    value = struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7fffffff
    return str(value % (10 ** digits)).zfill(digits)


def parse_accounts(text):
    if not isinstance(text, str) or len(text.encode()) > 512 * 1024:
        raise ValueError('导入内容不能为空或超过 512 KB')
    text = text.lstrip('\ufeff').strip()
    if text.startswith('['):
        try: rows = json.loads(text)
        except Exception: raise ValueError('账号 JSON 格式不正确') from None
    else:
        rows = []
        for number, line in enumerate(text.splitlines(), 1):
            if not line.strip(): continue
            try:
                delimiter = next((d for d in ['----', '\t', '|'] if d in line), None)
                parts = line.split(delimiter) if delimiter else next(csv.reader([line]))
                if len(parts) != 3: raise ValueError()
                rows.append(dict(zip(['email', 'password', 'totp'], parts)))
            except Exception:
                raise ValueError('第 ' + str(number) + ' 行须为邮箱、密码、2FA 密钥三个字段') from None
    if not isinstance(rows, list) or not 1 <= len(rows) <= 100:
        raise ValueError('每批支持 1–100 个账号')
    records, seen = [], set()
    for number, row in enumerate(rows, 1):
        try:
            if not isinstance(row, dict) or set(row) != {'email', 'password', 'totp'}: raise ValueError()
            if any(not isinstance(row[k], str) for k in row): raise ValueError()
            email = row['email'].strip().lower()
            if not re.fullmatch(r'[^\s@<>]{1,128}@[^\s@<>]{1,128}\.[^\s@<>]{1,64}', email): raise ValueError()
            password = row['password']
            if not 1 <= len(password) <= 1024 or any(c in password for c in '\r\n\x00'): raise ValueError()
            seed = normalize_seed(row['totp'].strip())
            if email in seen: raise ValueError()
        except Exception:
            raise ValueError('第 ' + str(number) + ' 条账号格式、2FA 密钥不正确，或邮箱重复') from None
        seen.add(email)
        records.append({'email': email, 'password': password, 'totp': seed})
    return records


class Store:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(self.directory, 0o700)
        self.path = self.directory / 'jobs.sqlite3'
        key = self.directory / 'credentials.key'
        if not key.exists():
            with os.fdopen(os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as stream:
                stream.write(Fernet.generate_key())
        self.cipher = Fernet(key.read_bytes())
        with self.connect() as db:
            db.execute('''CREATE TABLE IF NOT EXISTS jobs (
                id TEXT PRIMARY KEY,email TEXT NOT NULL,state TEXT NOT NULL,message TEXT NOT NULL,
                proxy_id INTEGER NOT NULL,proxy_name TEXT NOT NULL,account_id INTEGER,
                created_at REAL NOT NULL,updated_at REAL NOT NULL,secrets BLOB)''')
            db.execute("UPDATE jobs SET state='interrupted',message='服务已重启，请重新导入此账号',secrets=NULL,updated_at=? WHERE state IN ('logging_in','awaiting_human','verifying')", (time.time(),))
            db.execute('DELETE FROM jobs WHERE secrets IS NULL AND updated_at < ?', (time.time() - 7 * 86400,))
        os.chmod(self.path, 0o600)

    @contextmanager
    def connect(self):
        db = sqlite3.connect(self.path, timeout=10)
        db.row_factory = sqlite3.Row
        try:
            with db: yield db
        finally: db.close()

    def list(self):
        with self.connect() as db:
            return [dict(row) for row in db.execute('SELECT ' + PUBLIC_COLUMNS + ' FROM jobs ORDER BY created_at DESC LIMIT 500')]

    def get(self, identifier):
        with self.connect() as db:
            row = db.execute('SELECT ' + PUBLIC_COLUMNS + ' FROM jobs WHERE id=?', (identifier,)).fetchone()
            return dict(row) if row else None

    def enqueue(self, records, proxies):
        if not proxies: raise ValueError('没有可用代理，请选择具体出口或直连')
        created = []
        with self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            pending = {r[0] for r in db.execute('SELECT email FROM jobs WHERE secrets IS NOT NULL')}
            if pending.intersection(row['email'] for row in records): raise ValueError('部分邮箱已在待处理队列中')
            if len(pending) + len(records) > 200: raise ValueError('待处理任务不能超过 200 个')
            for index, record in enumerate(records):
                identifier = uuid.uuid4().hex
                proxy = proxies[index % len(proxies)]
                secret = self.cipher.encrypt(json.dumps({**record, 'proxy': proxy}).encode())
                now = time.time() + index * 0.00001
                db.execute('INSERT INTO jobs VALUES (?,?,?,?,?,?,NULL,?,?,?)',
                    (identifier, record['email'], 'queued', '等待登录', proxy['id'], proxy['name'], now, now, secret))
                created.append(identifier)
        return [self.get(identifier) for identifier in created]

    def credentials(self, identifier):
        with self.connect() as db:
            row = db.execute('SELECT secrets FROM jobs WHERE id=?', (identifier,)).fetchone()
        return json.loads(self.cipher.decrypt(row[0])) if row and row[0] else None

    def update(self, identifier, state, message, account_id=None):
        with self.connect() as db:
            placeholders = ','.join('?' for _ in TERMINAL)
            db.execute('UPDATE jobs SET state=?,message=?,account_id=?,updated_at=?,secrets=CASE WHEN ? THEN NULL ELSE secrets END WHERE id=? AND state NOT IN (' + placeholders + ')',
                (state, message, account_id, time.time(), state in TERMINAL, identifier, *TERMINAL))

    def clear_finished(self):
        with self.connect() as db: db.execute('DELETE FROM jobs WHERE secrets IS NULL')
