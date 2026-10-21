import asyncio
import base64
from pathlib import Path
import tempfile
import unittest

from core import Store, parse_accounts
from worker import Engine

SEED=base64.b32encode(b'12345678901234567890').decode()


class Native:
    def __init__(self): self.calls=[];self.reject=False
    def commit(self, credentials, cookie, identifier):
        self.calls.append((credentials['proxy']['id'],cookie,identifier))
        if self.reject: raise ValueError('验证未通过')
        return {'id':31,'duplicate':False}


class Session:
    instances=[]
    needs_human=False
    def __init__(self): self.closed=False;self.actions=[];self.instances.append(self)
    async def open(self,proxy): self.proxy=proxy
    async def login(self,credentials,pause,cancelled,progress):
        if self.needs_human: await pause('需要额外安全验证')
        if cancelled(): raise asyncio.CancelledError()
        return 'SAPISID=fake; __Secure-1PSID=fake'
    async def close(self): self.closed=True
    async def screenshot(self): return b'jpeg'
    async def input(self,action): self.actions.append(action)


class FlowTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.temporary=tempfile.TemporaryDirectory();self.store=Store(Path(self.temporary.name))
        self.native=Native();Session.instances=[];Session.needs_human=False
        self.engine=Engine(self.store,self.native,Session,human_timeout=0.1)
        self.job=self.store.enqueue(parse_accounts(f'a@example.com----password----{SEED}'),[{'id':7,'name':'node','url':'http://proxy:20007'}])[0]
    async def asyncTearDown(self): self.temporary.cleanup()

    async def wait_state(self,state):
        for _ in range(100):
            if self.store.get(self.job['id'])['state']==state: return
            await asyncio.sleep(.005)
        self.fail('state not reached: '+state)

    async def test_only_verified_cookie_commits_and_secrets_are_erased(self):
        await self.engine.execute(self.job)
        self.assertEqual(self.store.get(self.job['id'])['state'],'imported')
        self.assertEqual(self.native.calls[0][0],7)
        self.assertTrue(Session.instances[0].closed)
        self.assertIsNone(self.store.credentials(self.job['id']))

    async def test_extra_challenge_pauses_and_resumes_same_session(self):
        Session.needs_human=True
        task=asyncio.create_task(self.engine.execute(self.job))
        await self.wait_state('awaiting_human')
        self.assertEqual(self.native.calls,[])
        self.assertEqual(await self.engine.screen(self.job['id']),b'jpeg')
        await self.engine.input(self.job['id'],{'type':'click','x':10,'y':20})
        self.engine.resume(self.job['id']);await task
        self.assertEqual(len(Session.instances),1)
        self.assertEqual(self.store.get(self.job['id'])['state'],'imported')

    async def test_cancel_paused_job_never_imports_and_closes_browser(self):
        Session.needs_human=True
        task=asyncio.create_task(self.engine.execute(self.job))
        await self.wait_state('awaiting_human');self.engine.cancel(self.job['id']);await task
        self.assertEqual(self.store.get(self.job['id'])['state'],'cancelled')
        self.assertEqual(self.native.calls,[])
        self.assertTrue(Session.instances[0].closed)
        self.assertIsNone(self.store.credentials(self.job['id']))

    async def test_timeout_and_failed_verification_do_not_enable_accounts(self):
        Session.needs_human=True
        await self.engine.execute(self.job)
        self.assertEqual(self.store.get(self.job['id'])['state'],'expired')
        self.assertFalse(self.native.calls)
        Session.needs_human=False
        other=self.store.enqueue(parse_accounts(f'b@example.com----password----{SEED}'),[{'id':0,'name':'直连','url':''}])[0]
        self.native.reject=True;await self.engine.execute(other)
        self.assertEqual(self.store.get(other['id'])['state'],'failed')
        self.assertIsNone(self.store.credentials(other['id']))


if __name__=='__main__': unittest.main()
