"""A regular isolated browser; unexpected challenges always require a human."""
import asyncio
import math
import os
import re
import time
from urllib.parse import unquote, urlsplit

from core import totp

LOGIN_URL='https://accounts.google.com/ServiceLogin?continue=https%3A%2F%2Fgemini.google.com%2Fapp'
WIDTH, HEIGHT=1000,720


def allowed_url(value,main=False):
    parsed=urlsplit(value)
    if not main and parsed.scheme in ('data','blob','about'): return True
    host=(parsed.hostname or '').lower()
    domains=('google.com',) if main else ('google.com','gstatic.com','googleapis.com','googleusercontent.com','ggpht.com','recaptcha.net')
    return parsed.scheme=='https' and any(host==domain or host.endswith('.'+domain) for domain in domains)


def proxy_settings(proxy):
    if not proxy['url']: return None
    parsed=urlsplit(proxy['url'])
    if parsed.scheme not in ('http','https','socks5') or not parsed.hostname or not parsed.port: raise ValueError('代理格式不支持浏览器登录')
    host='['+parsed.hostname+']' if ':' in parsed.hostname else parsed.hostname
    settings={'server':parsed.scheme+'://'+host+':'+str(parsed.port)}
    if parsed.username is not None: settings['username']=unquote(parsed.username)
    if parsed.password is not None: settings['password']=unquote(parsed.password)
    if parsed.scheme=='socks5' and parsed.username: raise ValueError('请选择 HTTP 代理入口')
    return settings


def serialize_cookies(cookies):
    selected={}
    for cookie in sorted(cookies,key=lambda c:(len(c.get('domain','')),len(c.get('path',''))),reverse=True):
        name,value=cookie['name'],cookie['value']
        if re.fullmatch(r'[A-Za-z0-9_!#$%&\'*+.^`|~-]+',name) and not any(c in value for c in '\r\n;'):
            selected.setdefault(name,value)
    if not selected.get('SAPISID') or not selected.get('__Secure-1PSID'): return None
    return '; '.join(name+'='+value for name,value in selected.items())


class BrowserSession:
    def __init__(self): self.manager=None;self.browser=None;self.context=None;self.page=None

    async def open(self,proxy):
        from playwright.async_api import async_playwright
        self.manager=await async_playwright().start()
        browser_env={name:value for name,value in os.environ.items() if not any(part in name.upper() for part in ('KEY','TOKEN','SECRET','PASSWORD'))}
        self.browser=await self.manager.chromium.launch(headless=False,args=['--disable-dev-shm-usage'],env=browser_env)
        settings=proxy_settings(proxy)
        self.context=await self.browser.new_context(viewport={'width':WIDTH,'height':HEIGHT},locale='en-US',accept_downloads=False,
            **({'proxy':settings} if settings else {}))
        async def guard(route):
            request=route.request
            main=request.is_navigation_request() and request.frame.parent_frame is None
            if allowed_url(request.url,main): await route.continue_()
            else: await route.abort()
        await self.context.route('**/*',guard)
        self.page=await self.context.new_page()
        self.page.set_default_timeout(12000)

    async def visible(self,selector):
        locator=self.page.locator(selector).first
        return locator if await locator.count() and await locator.is_visible() else None

    async def next(self,selector):
        button=await self.visible(selector)
        if button: await button.click(timeout=10000)
        else:
            candidate=self.page.get_by_role('button',name=re.compile(r'^(Next|下一步|繼續|继续)$')).first
            if await candidate.count() and await candidate.is_visible(): await candidate.click(timeout=10000)

    async def login(self,credentials,pause,cancelled,progress):
        await self.page.goto(LOGIN_URL,wait_until='domcontentloaded',timeout=60000)
        return await self.drive(credentials,pause,cancelled,progress)

    async def drive(self,credentials,pause,cancelled,progress):
        attempted=set();last_action=time.monotonic();deadline=time.monotonic()+180
        totp_attempts=0;last_totp_step=None
        while True:
            if cancelled(): raise asyncio.CancelledError()
            host=urlsplit(self.page.url).hostname or ''
            if host=='gemini.google.com':
                cookie=serialize_cookies(await self.context.cookies('https://gemini.google.com/app'))
                if cookie: return cookie
            if not allowed_url(self.page.url,main=True):
                await pause('登录页面需要人工检查，请接手验证或取消任务')
                last_action=time.monotonic();deadline=time.monotonic()+180
                continue
            if host=='accounts.google.com':
                email=await self.visible('#identifierId')
                password=await self.visible('input[name="Passwd"],input[type="password"]')
                code=await self.visible('input[name="totpPin"],#totpPin')
                if email and 'email' not in attempted:
                    progress('正在填写邮箱');await email.fill(credentials['email']);attempted.add('email')
                    await self.next('#identifierNext');last_action=time.monotonic()
                elif password and 'password' not in attempted:
                    progress('正在填写密码');await password.fill(credentials['password']);attempted.add('password')
                    await self.next('#passwordNext');last_action=time.monotonic()
                elif code and totp_attempts<2 and last_totp_step!=int(time.time())//30:
                    progress('正在生成并填写 2FA 验证码')
                    remaining=30-(time.time()%30)
                    if remaining<5: await asyncio.sleep(remaining+0.2)
                    await code.fill(totp(credentials['totp']));last_totp_step=int(time.time())//30;totp_attempts+=1
                    await self.next('#totpNext');last_action=time.monotonic()
                elif '/challenge/selection' in self.page.url:
                    authenticator=self.page.get_by_text(re.compile(r'Google Authenticator|身份验证器|驗證器')).first
                    if await authenticator.count() and await authenticator.is_visible() and 'choose-totp' not in attempted:
                        await authenticator.click();attempted.add('choose-totp');last_action=time.monotonic()
            if time.monotonic()-last_action>8 or time.monotonic()>deadline:
                await pause('需要额外安全验证或登录确认，请接手完成后点击“继续自动处理”')
                last_action=time.monotonic();deadline=time.monotonic()+180
            await asyncio.sleep(.5)

    async def screenshot(self):
        return await self.page.screenshot(type='jpeg',quality=65,timeout=10000)

    async def input(self,action):
        kind=action.get('type')
        if kind=='click':
            x,y=action.get('x'),action.get('y')
            if any(not isinstance(v,(float,int)) or not math.isfinite(v) for v in [x,y]) or not(0<=x<WIDTH and 0<=y<HEIGHT): raise ValueError('点击位置无效')
            await self.page.mouse.click(x,y)
        elif kind=='text':
            value=action.get('text')
            if not isinstance(value,str) or not 1<=len(value)<=2048: raise ValueError('输入内容过长或为空')
            await self.page.keyboard.insert_text(value)
        elif kind=='key':
            key=action.get('key')
            if key not in ('Tab','Shift+Tab','Enter','Backspace','Delete','Escape','ArrowUp','ArrowDown','ArrowLeft','ArrowRight','Control+A','Space'): raise ValueError('不支持此按键')
            await self.page.keyboard.press(key)
        elif kind=='wheel':
            delta=action.get('delta')
            if not isinstance(delta,(int,float)) or not math.isfinite(delta) or abs(delta)>1500: raise ValueError('滚动距离无效')
            await self.page.mouse.wheel(0,delta)
        else: raise ValueError('不支持此操作')

    async def close(self):
        if self.context: await self.context.close()
        if self.browser: await self.browser.close()
        if self.manager: await self.manager.stop()
