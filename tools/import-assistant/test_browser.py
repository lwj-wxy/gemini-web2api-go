import asyncio
import base64
import re
import time
import unittest

from playwright.async_api import async_playwright

from browser_login import BrowserSession, LOGIN_URL, allowed_url, proxy_settings
from core import totp

SEED=base64.b32encode(b'12345678901234567890').decode()


class BrowserTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.playwright=await async_playwright().start()
        self.browser=await self.playwright.chromium.launch(headless=True)
        self.context=await self.browser.new_context(viewport={'width':1000,'height':720})
        self.page=await self.context.new_page()
        self.session=BrowserSession();self.session.context=self.context;self.session.page=self.page
        self.requests=[];self.captured={};self.credentials={'email':'a@example.invalid','password':'fixture-password','totp':SEED}

    async def asyncTearDown(self): await self.browser.close();await self.playwright.stop()

    async def setup_routes(self,human=False):
        def capture(source,kind,value): self.captured[kind]=(value,time.time())
        await self.context.expose_binding('captureFixture',capture)
        async def route(request):
            url=request.request.url;self.requests.append(url)
            if 'ServiceLogin' in url:
                html='<input id="identifierId" type="email"><button id="identifierNext" onclick="captureFixture(\'email\',document.querySelector(\'input\').value).then(()=>location.href=\'https://accounts.google.com/password\')">Next</button>'
            elif '/password' in url:
                html='<input name="Passwd" type="password"><button id="passwordNext" onclick="captureFixture(\'password\',document.querySelector(\'input\').value).then(()=>location.href=\'https://accounts.google.com/challenge/totp\')">Next</button>'
            elif '/challenge/totp' in url:
                html='<input name="totpPin" id="totpPin"><button id="totpNext" onclick="captureFixture(\'totp\',document.querySelector(\'input\').value).then(()=>location.href=\'https://accounts.google.com/'+('human' if human else 'done')+'\')">Next</button>'
            elif '/human' in url:
                html='<h1>Additional verification</h1><input id="humanCode"><button id="humanNext" onclick="location.href=\'https://accounts.google.com/done\'">Continue</button>'
            elif '/done' in url:
                await self.context.add_cookies([
                    {'name':'SAPISID','value':'fixture-sapisid','domain':'.google.com','path':'/','secure':True,'httpOnly':True},
                    {'name':'__Secure-1PSID','value':'fixture-session','domain':'.google.com','path':'/','secure':True,'httpOnly':True}])
                html='<script>location.href="https://gemini.google.com/app"</script>'
            elif url=='https://gemini.google.com/app': html='<h1>Gemini fixture</h1>'
            else:
                await request.abort();return
            await request.fulfill(content_type='text/html',body='<!doctype html>'+html)
        await self.context.route('**/*',route)

    async def test_browser_fills_email_password_totp_and_captures_http_only_cookie(self):
        await self.setup_routes();steps=[]
        async def pause(message): self.fail('Unexpected manual pause')
        cookie=await self.session.login(self.credentials,pause,lambda:False,steps.append)
        self.assertIn('SAPISID=fixture-sapisid',cookie)
        self.assertIn('__Secure-1PSID=fixture-session',cookie)
        self.assertTrue(any('2FA' in step for step in steps))
        self.assertEqual(self.captured['email'][0],self.credentials['email'])
        self.assertEqual(self.captured['password'][0],self.credentials['password'])
        self.assertEqual(self.captured['totp'][0],totp(SEED,self.captured['totp'][1]))
        self.assertTrue(all(url.startswith('https://accounts.google.com/') or url=='https://gemini.google.com/app' for url in self.requests))

    async def test_manual_screen_input_and_resume_same_browser(self):
        await self.setup_routes(human=True);pauses=[]
        async def pause(message):
            pauses.append(message)
            self.assertIn('/human',self.page.url)
            screenshot=await self.session.screenshot();self.assertTrue(screenshot.startswith(b'\xff\xd8'))
            await self.page.locator('#humanCode').click()
            await self.session.input({'type':'text','text':'manual-fixture-code'})
            self.assertEqual(await self.page.locator('#humanCode').input_value(),'manual-fixture-code')
            await self.page.locator('#humanNext').click()
        cookie=await self.session.login(self.credentials,pause,lambda:False,lambda message:None)
        self.assertEqual(len(pauses),1)
        self.assertIn('fixture-session',cookie)

    async def test_headed_browser_starts_and_closes_with_ephemeral_profile(self):
        headed=BrowserSession()
        try:
            await headed.open({'url':''})
            await headed.context.route('**/*',lambda route:route.fulfill(content_type='text/html',body='<input id="identifierId" type="email">'))
            await headed.page.goto(LOGIN_URL)
            self.assertIsNotNone(await headed.visible('#identifierId'))
            self.assertTrue((await headed.screenshot()).startswith(b'\xff\xd8'))
        finally: await headed.close()

    async def test_navigation_and_remote_input_are_bounded(self):
        self.assertFalse(allowed_url('http://127.0.0.1/admin',True))
        self.assertFalse(allowed_url('https://google.com.attacker.example/',True))
        self.assertFalse(allowed_url('file:///etc/passwd',False))
        self.assertTrue(allowed_url('https://accounts.google.com/challenge',True))
        self.assertEqual(proxy_settings({'url':'http://user:pass@proxy:20001'})['server'],'http://proxy:20001')
        for action in [{'type':'click','x':-1,'y':0},{'type':'key','key':'F12'},{'type':'navigate','url':'file:///etc/passwd'}]:
            with self.assertRaises(ValueError): await self.session.input(action)

    async def test_google_identity_dependency_is_allowed_only_as_subresource(self):
        self.assertTrue(allowed_url('https://accounts.youtube.com/accounts/CheckConnection',False))
        self.assertFalse(allowed_url('https://accounts.youtube.com/accounts/CheckConnection',True))
        self.assertFalse(allowed_url('https://accounts.youtube.com.attacker.example/',False))
        self.assertFalse(allowed_url('https://www.youtube.com/',False))

    async def test_browser_rejection_is_not_offered_as_manual_verification(self):
        async def route(request):
            if 'ServiceLogin' in request.request.url:
                html='<input id="identifierId"><button id="identifierNext" onclick="location.href=\'https://accounts.google.com/v3/signin/rejected\'">Next</button>'
            else:html='<h1>Couldn\'t sign you in</h1><p>This browser or app may not be secure.</p><p>Try using a different browser.</p>'
            await request.fulfill(content_type='text/html',body=html)
        await self.context.route('**/*',route)
        async def pause(message):self.fail('Browser rejection must not request a verification code')
        with self.assertRaisesRegex(ValueError,'Google 拒绝当前浏览器'):
            await self.session.login(self.credentials,pause,lambda:False,lambda message:None)


if __name__=='__main__': unittest.main()
