import asyncio
import hashlib
import json
import urllib.request
import urllib.error

from cryptography.fernet import InvalidToken

from core import TERMINAL
from browser_login import LoginFailure


class NativeGateway:
    def __init__(self, base, admin_key, service_key):
        self.base=base.rstrip('/');self.admin_key=admin_key;self.service_key=service_key

    def request(self,path,body=None):
        request=urllib.request.Request(self.base+path,data=json.dumps(body).encode() if body is not None else None,
            headers={'Authorization':'Bearer '+self.admin_key,'Content-Type':'application/json',
                     'X-Gemini-Import':'1','X-Import-Assistant-Key':self.service_key})
        try:
            with urllib.request.urlopen(request,timeout=90) as response: return json.load(response)
        except urllib.error.HTTPError as error:
            if error.code in (400,409,422): raise ValueError('登录态或固定出口检测未通过，账号未入池') from None
            raise ValueError('账号池暂不可用，请稍后重新导入') from None
        except Exception: raise ValueError('无法连接账号池，请稍后重新导入') from None

    def proxies(self):
        data=self.request('/admin/api/proxies')
        return [{'id':p['id'],'name':p['name'],'url':p['url']} for p in data['items'] if p['enabled'] and p.get('fail_count',0)<5]

    def commit(self,credentials,cookie,identifier):
        return self.request('/admin/api/cookies/import-verified',{
            'email':credentials['email'],'cookie':cookie,'job_id':identifier,
            'proxy_id':credentials['proxy']['id'],
            'proxy_url_hash':hashlib.sha256(credentials['proxy']['url'].encode()).hexdigest()})


class Engine:
    def __init__(self,store,native,session_factory,human_timeout=1800):
        self.store=store;self.native=native;self.session_factory=session_factory
        self.human_timeout=human_timeout;self.active_id=None;self.session=None;self.signal=None
        self.task=None;self.cancel_event=None;self.lock=asyncio.Lock()
        self.stopping=False

    async def run(self):
        while not self.stopping:
            queued=[j for j in self.store.list() if j['state']=='queued']
            if queued: await self.execute(min(queued,key=lambda j:j['created_at']))
            await asyncio.sleep(1)

    async def execute(self,job):
        identifier=job['id']
        try: credentials=self.store.credentials(identifier)
        except (InvalidToken,ValueError):
            self.store.update(identifier,'failed','待处理凭据无法读取，请重新导入此账号')
            return
        if not credentials or self.store.get(identifier)['state']!='queued': return
        self.active_id=identifier;self.signal=asyncio.Event();self.cancel_event=asyncio.Event()
        self.session=self.session_factory()
        def cancelled(): return self.cancel_event.is_set() or self.store.get(identifier)['state']=='cancelled'
        def progress(message): self.store.update(identifier,'logging_in',message)
        async def pause(message):
            self.signal.clear()
            self.store.update(identifier,'awaiting_human',message)
            try: await asyncio.wait_for(self.signal.wait(),self.human_timeout)
            except asyncio.TimeoutError:
                self.store.update(identifier,'expired','人工验证等待超过 30 分钟，已释放浏览器')
                raise asyncio.CancelledError()
            if cancelled(): raise asyncio.CancelledError()
            progress('继续完成登录')
        async def perform():
            progress('正在打开独立登录窗口')
            await self.session.open(credentials['proxy'])
            cookie=await self.session.login(credentials,pause,cancelled,progress)
            if cancelled(): return
            self.store.update(identifier,'verifying','正在通过登录时的出口检测 Cookie')
            result=await asyncio.to_thread(self.native.commit,credentials,cookie,identifier)
            self.store.update(identifier,'already_present' if result.get('duplicate') else 'imported',
                '账号已存在，保留原设置' if result.get('duplicate') else '检测通过，已加入 Cookie 池',result['id'])
        action=asyncio.create_task(perform());cancel_wait=asyncio.create_task(self.cancel_event.wait())
        try:
            done,_=await asyncio.wait([action,cancel_wait],return_when=asyncio.FIRST_COMPLETED)
            if cancel_wait in done and not action.done(): action.cancel()
            await action
        except asyncio.CancelledError:
            current=self.store.get(identifier)
            if current and current['state'] not in TERMINAL: self.store.update(identifier,'interrupted','登录已中断，请重新导入')
        except LoginFailure as error:
            self.store.update(identifier,error.state,str(error))
        except ValueError:
            self.store.update(identifier,'failed','登录态或固定出口检测未通过，未入池；请检查后重新导入')
        except Exception:
            self.store.update(identifier,'failed','登录或浏览器连接失败，请检查账号和出口后重新导入')
        finally:
            if not action.done(): action.cancel()
            cancel_wait.cancel()
            await asyncio.gather(action,cancel_wait,return_exceptions=True)
            try: await self.session.close()
            except Exception: pass
            credentials.clear()
            self.active_id=None;self.session=None;self.signal=None;self.cancel_event=None

    def cancel(self,identifier):
        job=self.store.get(identifier)
        if not job: raise ValueError('任务不存在')
        if job['state']=='verifying': raise ValueError('正在检测入池，请等待本次结果')
        if job['state'] in TERMINAL: return
        self.store.update(identifier,'cancelled','已取消，凭据已清除')
        if self.active_id==identifier:
            self.cancel_event.set();self.signal.set()

    def resume(self,identifier):
        if self.active_id!=identifier or self.store.get(identifier)['state']!='awaiting_human': raise ValueError('任务当前不在人工验证阶段')
        self.signal.set()

    def require_human(self,identifier):
        if self.active_id!=identifier or self.store.get(identifier)['state']!='awaiting_human': raise ValueError('此任务没有可接手的登录窗口')

    async def screen(self,identifier):
        self.require_human(identifier)
        async with self.lock: return await self.session.screenshot()

    async def input(self,identifier,action):
        self.require_human(identifier)
        async with self.lock: await self.session.input(action)
