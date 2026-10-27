import contextlib
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch
from subscription_web import SubscriptionWorker, SubscriptionError, resolve_public_target, read_request, download_subscription

YAML='proxies: [{name: Example, type: http, server: example.com, port: 443}]'

class SubscriptionWorkerTests(unittest.TestCase):
    def fixture(self, download=lambda url:YAML, sync=None, restore=None):
        temporary=tempfile.TemporaryDirectory(dir=pathlib.Path.cwd());self.addCleanup(temporary.cleanup)
        base=pathlib.Path(temporary.name);root=base/'manager';native=base/'native';root.mkdir();native.mkdir()
        (root/'subscription.url').write_text('https://old.example/sub?token=old-private')
        if sync is None:sync=lambda **kwargs:{'phase':'ready','nodes':1,'applications':{'gemini':{'healthy_nodes':1,'excluded_nodes':0}}}
        worker=SubscriptionWorker(root,native,download=download,sync=sync,restore=restore or (lambda names:0),lock=lambda:contextlib.nullcontext())
        return root,native,worker

    def request(self,url='https://new.example/sub?token=private',restore=False):
        return {'job_id':'a'*64,'url':url,'restore_deleted':restore,'created_at':1}

    def test_validated_update_runs_sync_and_publishes_no_url_or_tokens(self):
        seen=[]
        def sync(**kwargs):
            seen.append(kwargs)
            return {'phase':'ready','nodes':1,'applications':{'gemini':{'healthy_nodes':1,'excluded_nodes':0}}}
        root,native,worker=self.fixture(sync=sync)
        worker.process(self.request())
        status=json.loads((native/'proxy-subscription-status.json').read_text())
        self.assertEqual(status['state'],'succeeded');self.assertEqual(status['imported'],1)
        self.assertEqual((root/'subscription.url').read_text().strip(),'https://new.example/sub?token=private')
        self.assertNotIn('token=',json.dumps(status));self.assertNotIn('private',json.dumps(status))
        self.assertEqual(len(seen),1)
        self.assertFalse(seen[0]['force'])

    def test_invalid_subscription_keeps_working_url_and_never_syncs(self):
        root,native,worker=self.fixture(download=lambda url:'not a Clash subscription',sync=lambda **kwargs:self.fail('invalid subscription synchronized'))
        worker.process(self.request())
        self.assertIn('old.example',(root/'subscription.url').read_text())
        self.assertEqual(json.loads((native/'proxy-subscription-status.json').read_text())['state'],'failed')

    def test_restore_is_explicit_and_scoped_to_validated_new_subscription(self):
        restored=[]
        root,native,worker=self.fixture(restore=lambda names:restored.extend(names) or len(names))
        worker.process(self.request(restore=False));self.assertFalse(restored)
        worker.process(self.request(restore=True))
        self.assertEqual(len(restored),1);self.assertTrue(restored[0].startswith('to-mihomo:'))
        self.assertEqual(json.loads((native/'proxy-subscription-status.json').read_text())['restored'],1)

    def test_blank_url_refreshes_existing_subscription(self):
        seen=[]
        root,native,worker=self.fixture(download=lambda url:seen.append(url) or YAML)
        worker.process(self.request(url=''))
        self.assertEqual(seen,['https://old.example/sub?token=old-private'])
        self.assertEqual(json.loads((native/'proxy-subscription-status.json').read_text())['source_host'],'old.example')

    def test_sync_failures_and_zero_imports_are_not_reported_as_success(self):
        for result,state in [({'phase':'error'},'failed'),({'phase':'ready','nodes':1,'applications':{'gemini':{'healthy_nodes':0,'excluded_nodes':1}}},'warning')]:
            with self.subTest(state=state):
                root,native,worker=self.fixture(sync=lambda **kwargs:result)
                worker.process(self.request())
                self.assertEqual(json.loads((native/'proxy-subscription-status.json').read_text())['state'],state)

    def test_request_file_has_only_allowed_fields_and_is_consumed(self):
        root,native,worker=self.fixture()
        file=native/'proxy-subscription-request.json';file.write_text(json.dumps(self.request()))
        request=read_request(file,expected_uid=None)
        self.assertEqual(request['job_id'],'a'*64);self.assertFalse(file.exists())
        file.write_text(json.dumps(dict(self.request(),command='id')))
        with self.assertRaises(SubscriptionError):read_request(file,expected_uid=None)
        self.assertFalse(file.exists())

    def test_dns_and_redirect_targets_cannot_reach_private_or_metadata_addresses(self):
        def resolver(host,port,*args):return [(2,1,6,'',(host,port))]
        for address in ['127.0.0.1','10.0.0.1','169.254.169.254','100.64.0.1','0.0.0.0','localhost']:
            with self.subTest(address=address):
                with self.assertRaises(SubscriptionError):resolve_public_target('https://'+address+'/sub',resolver)
        host,port,path,addresses=resolve_public_target('https://1.1.1.1:22696/sub?token=x',resolver)
        self.assertEqual((host,port,path),('1.1.1.1',22696,'/sub?token=x'));self.assertEqual(addresses,['1.1.1.1'])

    def test_download_redirect_cannot_fetch_metadata_or_downgrade_https(self):
        for target in ['https://169.254.169.254/latest/meta-data','http://1.1.1.1/sub']:
            with self.subTest(target=target):
                calls=[]
                class Response:
                    status=302
                    def getheader(self,name,default=None):return target if name=='Location' else default
                class Connection:
                    def __init__(self,*args):calls.append(args)
                    def request(self,*args,**kwargs):pass
                    def getresponse(self):return Response()
                    def close(self):pass
                resolver=lambda host,port,*args:[(2,1,6,'',(host,port))]
                with patch('subscription_web.PinnedHTTPSConnection',Connection),patch('subscription_web.resolve_public_target',side_effect=lambda raw:resolve_public_target(raw,resolver)):
                    with self.assertRaises(SubscriptionError):download_subscription('https://1.1.1.1/sub')
                self.assertEqual(len(calls),1)

    def test_download_uses_validated_ip_and_rejects_oversized_response(self):
        calls=[]
        class Response:
            status=200
            def getheader(self,name,default=None):return '999999999' if name=='Content-Length' else default
        class Connection:
            def __init__(self,*args):calls.append(args)
            def request(self,*args,**kwargs):pass
            def getresponse(self):return Response()
            def close(self):pass
        resolver=lambda host,port,*args:[(2,1,6,'',('1.1.1.1',port))]
        with patch('subscription_web.PinnedHTTPSConnection',Connection),patch('subscription_web.resolve_public_target',side_effect=lambda raw:resolve_public_target(raw,resolver)):
            with self.assertRaises(SubscriptionError):download_subscription('https://subscription.example:22696/sub')
        self.assertEqual(calls[0][:3],('subscription.example',22696,'1.1.1.1'))

    def test_download_reads_valid_subscription_without_proxy_environment(self):
        class Response:
            status=200
            def __init__(self):self.sent=False
            def getheader(self,name,default=None):return default
            def read1(self,limit):
                if self.sent:return b''
                self.sent=True;return YAML.encode()
        class Connection:
            def __init__(self,*args):pass
            def request(self,*args,**kwargs):pass
            def getresponse(self):return Response()
            def close(self):pass
        with patch('subscription_web.PinnedHTTPSConnection',Connection),patch('subscription_web.resolve_public_target',return_value=('subscription.example',443,'/sub',['1.1.1.1'])):
            self.assertEqual(download_subscription('https://subscription.example/sub'),YAML)

if __name__=='__main__':unittest.main()
