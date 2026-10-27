"""One-shot subscription configuration worker; no HTTP or command executor.

The authenticated native admin queues a bounded JSON file in its existing data
directory. systemd.path invokes this fixed worker. Only sanitized progress is
published back; the request containing the URL is consumed immediately.
"""
import contextlib
import gzip
import hashlib
import http.client
import io
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import ssl
import stat
import tempfile
import threading
import time
import urllib.parse

from core import PREFIX, node_key, parse_subscription
from manager import ROOT, atomic, read, run as synchronize

NATIVE = Path('/opt/gemini-web2api-go/data')
LIMIT = 4 * 1024 * 1024
ACTIVE = {'queued','waiting','validating','syncing'}

class SubscriptionError(ValueError):
    pass

def resolve_public_target(raw, resolver=socket.getaddrinfo):
    if not isinstance(raw,str) or not raw or len(raw)>4096 or any(ord(c)<32 or ord(c)==127 for c in raw):
        raise SubscriptionError('invalid_url')
    try:
        parsed=urllib.parse.urlsplit(raw)
        host=(parsed.hostname or '').rstrip('.').encode('idna').decode('ascii')
        port=parsed.port or 443
    except (ValueError,UnicodeError):raise SubscriptionError('invalid_url') from None
    if parsed.scheme!='https' or not host or parsed.username is not None or parsed.password is not None or parsed.fragment or not 1<=port<=65535:
        raise SubscriptionError('invalid_url')
    if host.lower()=='localhost' or host.lower().endswith(('.localhost','.local','.internal','.home.arpa')):
        raise SubscriptionError('private_address')
    try: answers=resolver(host,port,socket.AF_UNSPEC,socket.SOCK_STREAM)
    except OSError:raise SubscriptionError('download_failed') from None
    addresses=[]
    for answer in answers:
        address=answer[4][0]
        try: valid=ipaddress.ip_address(address).is_global
        except ValueError:valid=False
        if not valid:raise SubscriptionError('private_address')
        if address not in addresses:addresses.append(address)
    if not addresses:raise SubscriptionError('download_failed')
    addresses.sort(key=lambda value:':' in value)
    target=urllib.parse.urlunsplit(('', '', parsed.path or '/', parsed.query, ''))
    return host,port,target,addresses[:2]

class PinnedHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self,host,port,address,timeout):
        super().__init__(host,port,timeout=timeout,context=ssl.create_default_context())
        self.address=address
    def connect(self):
        # Validate DNS once, connect to that address, retain verified hostname
        # for SNI/certificate checks. Do not re-resolve or trust proxy env vars.
        raw=socket.create_connection((self.address,self.port),self.timeout)
        try:self.sock=self._context.wrap_socket(raw,server_hostname=self.host)
        except Exception:raw.close();raise

def download_subscription(url):
    deadline=time.monotonic()+45
    current=url
    for redirect in range(4):
        host,port,target,addresses=resolve_public_target(current)
        response=None;connection=None
        for address in addresses:
            remaining=deadline-time.monotonic()
            if remaining<=0:raise SubscriptionError('download_failed')
            connection=PinnedHTTPSConnection(host,port,address,min(12,remaining))
            try:
                connection.request('GET',target,headers={'User-Agent':'clash.meta','Accept-Encoding':'identity'})
                response=connection.getresponse();break
            except (OSError,http.client.HTTPException):connection.close()
        if response is None:raise SubscriptionError('download_failed')
        try:
            if response.status in (301,302,303,307,308):
                location=response.getheader('Location')
                if not location or redirect==3:raise SubscriptionError('download_failed')
                current=urllib.parse.urljoin(current,location)
                # The next hop is validated and pinned before any connection.
                continue
            if response.status!=200:raise SubscriptionError('download_failed')
            length=response.getheader('Content-Length')
            if length and (not length.isdigit() or int(length)>LIMIT):raise SubscriptionError('subscription_too_large')
            chunks=[];total=0
            while True:
                if time.monotonic()>deadline:raise SubscriptionError('download_failed')
                chunk=response.read1(min(65536,LIMIT+1-total))
                if not chunk:break
                chunks.append(chunk);total+=len(chunk)
                if total>LIMIT:raise SubscriptionError('subscription_too_large')
            raw=b''.join(chunks)
            if response.getheader('Content-Encoding','').lower()=='gzip':
                with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:raw=stream.read(LIMIT+1)
                if len(raw)>LIMIT:raise SubscriptionError('subscription_too_large')
            try:return raw.decode('utf-8-sig')
            except UnicodeError:raise SubscriptionError('invalid_subscription') from None
        except (OSError,http.client.HTTPException):raise SubscriptionError('download_failed') from None
        finally:connection.close()
    raise SubscriptionError('download_failed')

def read_request(path,expected_uid=65532):
    path=Path(path)
    try:
        flags=os.O_RDONLY|getattr(os,'O_NOFOLLOW',0)|getattr(os,'O_NONBLOCK',0)
        fd=os.open(path,flags)
        with os.fdopen(fd,'r',encoding='utf-8') as stream:
            info=os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size>8192 or info.st_nlink!=1 or (expected_uid is not None and os.name=='posix' and info.st_uid!=expected_uid):
                raise SubscriptionError('invalid_request')
            value=json.load(stream)
    except (OSError,ValueError):raise SubscriptionError('invalid_request') from None
    finally:
        # A bad request must not keep systemd.path restarting indefinitely.
        path.unlink(missing_ok=True)
    if not isinstance(value,dict) or set(value)!={'job_id','url','restore_deleted','created_at'}:
        raise SubscriptionError('invalid_request')
    if not isinstance(value['job_id'],str) or not re.fullmatch('[0-9a-f]{64}',value['job_id']) or not isinstance(value['url'],str) or len(value['url'])>4096 or type(value['restore_deleted']) is not bool or type(value['created_at']) is not int:
        raise SubscriptionError('invalid_request')
    return value

def publish_status(directory,status):
    directory=Path(directory);directory.mkdir(parents=True,exist_ok=True)
    fd,name=tempfile.mkstemp(prefix='.proxy-subscription-status-',dir=directory)
    try:
        with os.fdopen(fd,'w',encoding='utf-8') as stream:
            json.dump(status,stream,ensure_ascii=False);stream.flush();os.fsync(stream.fileno())
            # Status contains counts, a hostname and codes, never URL tokens.
            if os.name=='posix':os.fchmod(stream.fileno(),0o644)
        os.replace(name,directory/'proxy-subscription-status.json')
    finally:
        if os.path.exists(name):os.unlink(name)

def restore_gemini(names):
    from manager import env
    from adapters import API
    token=env('/opt/gemini-web2api-go/.env')['ADMIN_TOKEN']
    api=API('http://127.0.0.1:18000',token)
    try:return api.request('DELETE','/admin/api/proxy-exclusions',{'names':names})['restored']
    except Exception:raise SubscriptionError('restore_failed') from None
    finally:api.close()

class SubscriptionWorker:
    def __init__(self,root=ROOT,native=NATIVE,download=download_subscription,sync=synchronize,restore=restore_gemini,lock=None):
        self.root=Path(root);self.native=Path(native);self.download=download;self.sync=sync;self.restore=restore
        self.lock=lock or self.sync_lock
        self.guard=threading.RLock();self.status={};self.sync_started=0

    def initial_status(self):
        raw=(self.root/'subscription.url').read_text().strip() if (self.root/'subscription.url').exists() else ''
        try:host=urllib.parse.urlsplit(raw).hostname or ''
        except ValueError:host=''
        previous=read(self.root/'status.json',{})
        gemini=previous.get('applications',{}).get('gemini',{})
        return {'available':True,'configured':bool(raw),'source_host':host,'state':'idle','job_id':'','code':'','node_count':previous.get('nodes',0),'checked':0,'check_total':0,'imported':gemini.get('healthy_nodes',0),'excluded':gemini.get('excluded_nodes',0),'restored':0,'updated_at':int(time.time())}

    def update(self,**fields):
        with self.guard:
            self.status.update(fields);self.status['updated_at']=int(time.time())
            publish_status(self.native,self.status)

    @contextlib.contextmanager
    def sync_lock(self):
        import fcntl
        self.root.mkdir(parents=True,exist_ok=True)
        with (self.root/'sync.lock').open('a') as lock:
            deadline=time.monotonic()+1800
            while True:
                try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB);break
                except BlockingIOError:
                    if time.monotonic()>deadline:raise SubscriptionError('sync_busy')
                    time.sleep(.5)
            try:yield
            finally:fcntl.flock(lock,fcntl.LOCK_UN)

    def heartbeat(self,stop):
        while not stop.wait(2):
            with self.guard:
                fields={}
                if self.status.get('state')=='syncing':
                    current=read(self.root/'status.json',{})
                    if current.get('checked_at',0)>=self.sync_started:
                        fields={'checked':current.get('batch_completed',0),'check_total':current.get('batch_total',0)}
                self.update(**fields)

    def process(self,request):
        self.status=self.initial_status()
        self.update(job_id=request['job_id'],state='waiting',code='')
        stop=threading.Event();heartbeat=threading.Thread(target=self.heartbeat,args=(stop,),daemon=True);heartbeat.start()
        try:
            with self.lock():
                self.update(state='validating')
                current=request['url'].strip()
                if not current:
                    file=self.root/'subscription.url'
                    current=file.read_text().strip() if file.exists() else ''
                # Downloader performs URL/DNS/redirect validation. Keep the old
                # working configuration until download and parsing both pass.
                raw=self.download(current)
                try:nodes=parse_subscription(raw)
                except ValueError:raise SubscriptionError('invalid_subscription') from None
                self.update(node_count=len(nodes))
                atomic(self.root/'subscription.url',current+'\n')
                atomic(self.root/'cache.json',{'at':time.time(),'source':hashlib.sha256(current.encode()).hexdigest(),'nodes':nodes})
                if (self.root/'paused').exists():(self.root/'paused').unlink()
                if request['restore_deleted']:
                    names=[PREFIX+node_key(node) for node in nodes]
                    self.update(restored=self.restore(names))
                self.sync_started=int(time.time())
                self.update(configured=True,source_host=urllib.parse.urlsplit(current).hostname or '',state='syncing')
                result=self.sync(root=self.root,force=False,downloader=self.download)
                gemini=result.get('applications',{}).get('gemini',{})
                if result.get('phase')=='error' or 'error' in gemini:raise SubscriptionError('sync_failed')
                count=int(gemini.get('healthy_nodes',0));excluded=int(gemini.get('excluded_nodes',0))
                state='succeeded' if count>0 else 'warning'
                code='' if count>0 else ('deleted_nodes_skipped' if excluded else 'no_healthy_nodes')
                self.update(state=state,code=code,imported=count,excluded=excluded,checked=result.get('batch_completed',0),check_total=result.get('batch_total',0))
        except Exception as error:
            code=str(error) if isinstance(error,SubscriptionError) else 'sync_failed'
            if code not in {'invalid_url','private_address','download_failed','invalid_subscription','subscription_too_large','sync_failed','sync_busy','restore_failed'}:code='sync_failed'
            self.update(state='failed',code=code)
        finally:
            request['url']='';stop.set();heartbeat.join(timeout=5)
            self.update()

def main():
    import argparse
    parser=argparse.ArgumentParser();parser.add_argument('action',choices=['run','publish']);args=parser.parse_args()
    worker=SubscriptionWorker()
    if args.action=='publish':publish_status(worker.native,worker.initial_status());return
    try:request=read_request(worker.native/'proxy-subscription-request.json')
    except SubscriptionError:
        status=worker.initial_status();status.update(state='failed',code='invalid_request');publish_status(worker.native,status);return
    worker.process(request)

if __name__=='__main__':main()
