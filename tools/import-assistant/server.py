import asyncio
from contextlib import asynccontextmanager
import hmac
import json
import os
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.responses import HTMLResponse, JSONResponse, Response

from browser_login import BrowserSession
from core import Store, parse_accounts
from worker import Engine, NativeGateway


def create_app(store=None,native=None,key=None,session_factory=BrowserSession,start_worker=True):
    engine=None
    service_key=key if key is not None else os.environ.get('IMPORT_ASSISTANT_KEY','')
    @asynccontextmanager
    async def lifespan(app):
        nonlocal store,native,engine
        if not service_key: raise RuntimeError('Import assistant authentication is not configured')
        store=store or Store(Path(os.environ.get('IMPORT_ASSISTANT_STATE','/state')))
        native=native or NativeGateway(os.environ.get('GEMINI_BASE_URL','http://gemini-web2api:8083'),os.environ['GEMINI_ADMIN_TOKEN'],service_key)
        engine=Engine(store,native,session_factory)
        app.state.engine=engine
        task=asyncio.create_task(engine.run()) if start_worker else None
        yield
        engine.stopping=True
        if task:
            task.cancel();await asyncio.gather(task,return_exceptions=True)
    app=FastAPI(docs_url=None,redoc_url=None,openapi_url=None,lifespan=lifespan)

    @app.middleware('http')
    async def protect(request,call_next):
        if request.url.path=='/healthz': return JSONResponse({'status':'ok'})
        if not service_key or not hmac.compare_digest(request.headers.get('X-Import-Assistant-Key','').encode(),service_key.encode()):
            return JSONResponse({'error':'unauthorized'},status_code=401)
        length=request.headers.get('content-length','0')
        if not length.isdigit() or int(length)>2*1024*1024: return JSONResponse({'error':'请求过大'},status_code=413)
        try: response=await call_next(request)
        except Exception: response=JSONResponse({'error':'操作未完成，请重试'},status_code=500)
        response.headers['Cache-Control']='no-store'
        response.headers['X-Frame-Options']='SAMEORIGIN'
        response.headers['Content-Security-Policy']="default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'"
        return response

    async def body(request):
        raw=await request.body()
        if len(raw)>2*1024*1024: raise ValueError('请求过大')
        try: value=json.loads(raw)
        except Exception: raise ValueError('请求格式不正确') from None
        if not isinstance(value,dict): raise ValueError('请求格式不正确')
        return value

    @app.exception_handler(ValueError)
    async def invalid(request,error): return JSONResponse({'error':str(error)},status_code=400)

    @app.get('/',response_class=HTMLResponse)
    async def home(): return Path(__file__).with_name('assistant.html').read_text()

    @app.get('/api/jobs')
    async def list_jobs(): return {'items':store.list(),'active_id':engine.active_id}

    @app.get('/api/proxies')
    async def proxies():
        items=await asyncio.to_thread(native.proxies)
        return {'items':[{'id':p['id'],'name':p['name']} for p in items]}

    @app.post('/api/jobs')
    async def create_jobs(request:Request):
        payload=await body(request)
        if set(payload)!={'accounts','proxy_id'} or type(payload['proxy_id']) is not int: raise ValueError('请选择有效的登录出口')
        records=parse_accounts(payload['accounts']);identifier=payload['proxy_id']
        if identifier==0: candidates=[{'id':0,'name':'直连','url':''}]
        else:
            candidates=await asyncio.to_thread(native.proxies)
            if identifier!=-1: candidates=[p for p in candidates if p['id']==identifier]
        return JSONResponse({'items':store.enqueue(records,candidates)},status_code=202)

    @app.delete('/api/jobs')
    async def clear_jobs(): store.clear_finished();return {'ok':True}

    @app.post('/api/jobs/{identifier}/cancel')
    async def cancel_job(identifier:str): engine.cancel(identifier);return {'ok':True}

    @app.post('/api/jobs/{identifier}/resume')
    async def resume_job(identifier:str): engine.resume(identifier);return {'ok':True}

    @app.get('/api/jobs/{identifier}/screen')
    async def screen(identifier:str): return Response(await engine.screen(identifier),media_type='image/jpeg')

    @app.post('/api/jobs/{identifier}/input')
    async def remote_input(identifier:str,request:Request): await engine.input(identifier,await body(request));return {'ok':True}

    return app


app=create_app()
