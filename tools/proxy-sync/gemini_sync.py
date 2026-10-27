"""Gemini policy used by the host subscription synchronizer."""
from core import PREFIX, proxy_url

def sync_gemini(api,state,registry,candidates,save):
    snapshot=api.request('GET','/admin/api/proxies')
    rows=snapshot['items']
    excluded=set(snapshot.get('excluded_managed_names',[]))
    skipped=sum(PREFIX+key in excluded for key in candidates)
    candidates=[key for key in candidates if PREFIX+key not in excluded]
    owned={r['name'][len(PREFIX):]:r for r in rows if r['name'].startswith(PREFIX)}
    for key in candidates:
        name=PREFIX+key; url=proxy_url(registry[key]); row=owned.get(key)
        if row is None:
            result=api.request('POST','/admin/api/proxies',{'name':name,'url':url,'weight':1})
            row={'id':result['id'],'name':name,'url':url,'enabled':True}; owned[key]=row
        elif row.get('url')!=url or not row['enabled']:
            api.request('PATCH',f'/admin/api/proxies/{row["id"]}',{'url':url,'enabled':True})
        state.setdefault('nodes',{})[key]=row['id']; save()
    for key,row in owned.items():
        if key not in candidates and row['enabled']:
            api.request('PATCH',f'/admin/api/proxies/{row["id"]}',{'enabled':False})
    state['active_keys']=list(candidates); save()
    return {'healthy_nodes':len(candidates),'binding':'native_preferred_proxy_id','excluded_nodes':skipped}
