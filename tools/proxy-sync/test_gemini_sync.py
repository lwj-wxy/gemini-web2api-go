"""Run alongside the host synchronizer's existing core.py."""
import unittest
from gemini_sync import sync_gemini
from core import PREFIX

class GeminiDeletionTests(unittest.TestCase):
    def test_deleted_node_is_not_recreated_but_new_node_still_imports(self):
        class API:
            def __init__(self):self.calls=[]
            def request(self,method,path,body=None):
                self.calls.append((method,path,body))
                if method=='GET':return {'items':[],'excluded_managed_names':[PREFIX+'deleted']}
                if method=='POST':return {'id':7}
                raise AssertionError('unexpected mutation')
        api=API();state={}
        result=sync_gemini(api,state,{'deleted':{'port':20001},'new':{'port':20002}},['deleted','new'],lambda:None)
        self.assertEqual(result['excluded_nodes'],1)
        self.assertEqual(state['active_keys'],['new'])
        posts=[body for method,path,body in api.calls if method=='POST']
        self.assertEqual([body['name'] for body in posts],[PREFIX+'new'])

    def test_older_api_without_exclusions_stays_compatible(self):
        class API:
            def request(self,method,path,body=None):
                return {'items':[]} if method=='GET' else {'id':8}
        state={}
        sync_gemini(API(),state,{'new':{'port':20002}},['new'],lambda:None)
        self.assertEqual(state['active_keys'],['new'])

if __name__=='__main__':unittest.main()
