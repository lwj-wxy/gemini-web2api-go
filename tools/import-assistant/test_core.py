import base64
import json
from pathlib import Path
import tempfile
import unittest

from core import Store, parse_accounts, totp

SEED = base64.b32encode(b'12345678901234567890').decode()


class CoreTests(unittest.TestCase):
    def test_rfc_totp_vectors_and_six_digits(self):
        for timestamp, expected in [(59, '94287082'), (1111111109, '07081804'),
                                    (1111111111, '14050471'), (1234567890, '89005924'),
                                    (2000000000, '69279037'), (20000000000, '65353130')]:
            self.assertEqual(totp(SEED, timestamp, digits=8), expected)
        self.assertEqual(totp(SEED, 59), '287082')

    def test_parse_formats_and_preserve_password(self):
        for text in [f'a@example.com----pa ss----{SEED}', f'a@example.com|pa ss|{SEED}',
                     f'a@example.com\tpa ss\t{SEED}', f'a@example.com,"pa ss",{SEED}',
                     json.dumps([{'email': 'a@example.com', 'password': 'pa ss', 'totp': SEED}])]:
            records = parse_accounts(text)
            self.assertEqual(records, [{'email': 'a@example.com', 'password': 'pa ss', 'totp': SEED}])

    def test_google_import_ignores_separator_padding_preserving_internal_spaces(self):
        record=parse_accounts(f' a@example.com ---- pa ss ---- {SEED.lower()} ')[0]
        self.assertEqual(record['email'],'a@example.com')
        self.assertEqual(record['password'],'pa ss')
        self.assertEqual(record['totp'],SEED)

    def test_parse_otpauth_and_reject_duplicates_secrets_in_errors(self):
        self.assertEqual(parse_accounts(f'a@example.com----pass----otpauth://totp/account?secret={SEED}')[0]['totp'], SEED)
        for text in [f'a@example.com----secret-password----123456',
                     f'a@example.com----pass----{SEED}\na@example.com----pass----{SEED}',
                     f'wrong----pass----{SEED}', json.dumps([{'email':'a@example.com','password':'x','totp':SEED,'extra':'x'}])]:
            with self.assertRaises(ValueError) as error: parse_accounts(text)
            self.assertNotIn('secret-password', str(error.exception))
            self.assertNotIn(SEED, str(error.exception))

    def test_encrypted_queue_and_terminal_erasure(self):
        with tempfile.TemporaryDirectory() as temporary:
            store = Store(Path(temporary))
            item = store.enqueue(parse_accounts(f'a@example.com----private-password----{SEED}'), [{'id':0,'name':'直连','url':''}])[0]
            self.assertNotIn('password', json.dumps(store.list()))
            self.assertEqual(store.credentials(item['id'])['password'], 'private-password')
            raw = (Path(temporary)/'jobs.sqlite3').read_bytes()
            self.assertNotIn(b'private-password', raw)
            self.assertNotIn(SEED.encode(), raw)
            store.update(item['id'], 'cancelled', '已取消')
            self.assertIsNone(store.credentials(item['id']))
            self.assertEqual(store.get(item['id'])['state'],'cancelled')

    def test_restart_does_not_replay_running_login_and_proxy_is_fixed(self):
        with tempfile.TemporaryDirectory() as temporary:
            store=Store(Path(temporary))
            items=store.enqueue(parse_accounts(f'a@example.com----pass----{SEED}\nb@example.com----pass----{SEED}'),
                                [{'id':3,'name':'one','url':'http://proxy:20003'}, {'id':7,'name':'two','url':'http://proxy:20007'}])
            self.assertEqual([i['proxy_id'] for i in items], [3,7])
            store.update(items[0]['id'],'logging_in','正在登录')
            Store(Path(temporary))
            self.assertEqual(store.get(items[0]['id'])['state'],'interrupted')
            self.assertIsNone(store.credentials(items[0]['id']))
            self.assertEqual(store.get(items[1]['id'])['state'],'queued')
            self.assertEqual(store.credentials(items[1]['id'])['proxy']['url'],'http://proxy:20007')

    def test_pending_duplicate_is_rejected_atomically(self):
        with tempfile.TemporaryDirectory() as temporary:
            store=Store(Path(temporary));records=parse_accounts(f'a@example.com----pass----{SEED}')
            store.enqueue(records,[{'id':0,'name':'直连','url':''}])
            with self.assertRaises(ValueError): store.enqueue(records,[{'id':0,'name':'直连','url':''}])
            self.assertEqual(len(store.list()),1)

    def test_active_queue_is_not_hidden_by_newer_finished_history(self):
        with tempfile.TemporaryDirectory() as temporary:
            store=Store(Path(temporary))
            pending=store.enqueue(parse_accounts(f'a@example.com----pass----{SEED}'),[{'id':0,'name':'直连','url':''}])[0]
            with store.connect() as db:
                db.executemany("INSERT INTO jobs VALUES (lower(hex(randomblob(16))),'ended@example.invalid','cancelled','',0,'直连',NULL,?,?,NULL)",
                    [(pending['created_at']+i+1,pending['created_at']+i+1) for i in range(501)])
            self.assertTrue(any(job['id']==pending['id'] for job in store.list()),'Pending job must remain visible')
            self.assertEqual(len(store.list()),500)


if __name__ == '__main__': unittest.main()
