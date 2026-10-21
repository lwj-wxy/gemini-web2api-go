import base64
import json
from pathlib import Path
import tempfile
import unittest

from fastapi.testclient import TestClient

from core import Store
from server import create_app

SEED=base64.b32encode(b'12345678901234567890').decode()


class FakeNative:
    def proxies(self): return [{'id':4,'name':'fixed','url':'http://private-proxy:20004'}]


class ServerTests(unittest.TestCase):
    def setUp(self):
        self.temporary=tempfile.TemporaryDirectory();self.store=Store(Path(self.temporary.name))
        self.client=TestClient(create_app(self.store,FakeNative(),key='test-service-key',start_worker=False));self.client.__enter__()
        self.headers={'X-Import-Assistant-Key':'test-service-key'}
    def tearDown(self): self.client.__exit__(None,None,None);self.temporary.cleanup()

    def test_authentication_and_no_secret_echo(self):
        self.assertEqual(self.client.get('/api/jobs').status_code,401)
        self.assertEqual(self.client.post('/api/jobs',json={'accounts':'private-password'}).status_code,401)
        response=self.client.post('/api/jobs',json={'accounts':'a@example.com----private-password----'+SEED,'proxy_id':-1},headers=self.headers)
        self.assertEqual(response.status_code,202,response.text)
        self.assertNotIn('private-password',response.text);self.assertNotIn(SEED,response.text)
        self.assertNotIn('private-proxy',self.client.get('/api/proxies',headers=self.headers).text)
        self.assertEqual(response.headers['cache-control'],'no-store')

    def test_invalid_batch_is_atomic_and_error_is_sanitized(self):
        response=self.client.post('/api/jobs',json={'accounts':'a@example.com----private-password----123456','proxy_id':0},headers=self.headers)
        self.assertEqual(response.status_code,400);self.assertNotIn('private-password',response.text)
        self.assertEqual(self.store.list(),[])

    def test_cancel_clears_credentials_and_no_inactive_browser_access(self):
        response=self.client.post('/api/jobs',json={'accounts':'a@example.com----private-password----'+SEED,'proxy_id':4},headers=self.headers)
        identifier=response.json()['items'][0]['id']
        self.assertEqual(self.client.get('/api/jobs/'+identifier+'/screen',headers=self.headers).status_code,400)
        self.assertEqual(self.client.post('/api/jobs/'+identifier+'/cancel',headers=self.headers).status_code,200)
        self.assertIsNone(self.store.credentials(identifier))
        self.assertEqual(self.client.delete('/api/jobs',headers=self.headers).status_code,200)
        self.assertEqual(self.store.list(),[])


if __name__=='__main__': unittest.main()
