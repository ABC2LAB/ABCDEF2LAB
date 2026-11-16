#!/usr/bin/env python3
"""One owned, disposable product-Compose PID acceptance round.

Run only with the pids-model/pids-target fixtures and a new local-only install.
Credentials stay in memory; stdout contains only owned IDs, counts and limits.
Use a separate read-only cgroup observer in preparation (see workspace decision §7).
"""
import json,secrets,time,urllib.request,urllib.error,sys
from pathlib import Path
import argparse
p=argparse.ArgumentParser()
p.add_argument('install',type=Path)
p.add_argument('control_token',type=Path)
p.add_argument('--base',default='http://preparation:8787')
p.add_argument('--target-ip',default='172.31.253.20')
p.add_argument('--expected-tasks',type=int,default=48)
p.add_argument('--sample-children',type=int,default=-1)
a=p.parse_args()
root=a.install.parent
cfg=json.loads(a.install.read_text());control=a.control_token.read_text().strip();token=''
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*_args,**_kwargs):return None
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
def req(path,body=None,base=None,key=None,control_header=False):
 base=base or a.base
 headers={'Content-Type':'application/json'}
 if token and base==a.base:headers['Authorization']='Bearer '+token
 if control_header:headers['X-Fixture-Control']=control
 if key:headers['Idempotency-Key']=key
 r=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers=headers)
 try:
  with opener.open(r,timeout=20) as out:
   raw=out.read(2<<20);return json.loads(raw) if raw else {}
 except urllib.error.HTTPError as e:
  raw=e.read();
  try:d=json.loads(raw)
  except:d={}
  raise RuntimeError(str(e.code)+':'+path+':'+str(d.get('error','request_failed'))) from None

token=req('/api/auth/login',{'username':'ABC2LAB','password':cfg['admin_password']})['token']
target=req('/api/product/targets',{'url':'https://juice.test:9443/rest/products/search','path_prefixes':['/rest/products'],'methods':['GET','HEAD'],'readonly_basis':'Owned disposable PID acceptance: read-only synthetic target requests do not change application resources.','requests':400,'valid_hours':4})['target']
req('/control',{'name':target['challenge_name'],'values':[target['challenge_value']],'host':'juice.test','ip':a.target_ip},base='http://fixture-dns:9083',control_header=True)
req('/api/product/targets/'+target['id']+'/approve',{})
job=req('/api/diagnoses/v2/drafts',{'version':'diagnosis-api.v2','input':{'target_url':'https://juice.test:9443/rest/products/search','profile_id':'product-model-local_only','display_name':'Owned Compose PID acceptance'}},key='pr598-pids-create-'+secrets.token_hex(6))['draft']
prefix='/api/diagnoses/v2/drafts/'+job['id']
job=req(prefix+'/validate',{'version':'diagnosis-api.v2','expected_revision':job['revision']})['draft']
print(json.dumps({'validated_state':job['state'],'reason':job.get('reason_code')}),flush=True)
if job['state']!='ready':raise RuntimeError('fixture_not_ready')
started=req(prefix+'/start',{'version':'diagnosis-api.v2','expected_revision':job['revision']},key='pr598-pids-start-'+secrets.token_hex(6))
ident=job['id']
print(json.dumps({'started':True,'draft':job['id']}),flush=True)

def check(ok,code):
 if not ok:raise RuntimeError(code)
def stats():return req('/pids-stats',base='http://target-front:9081',control_header=True)
def release(phase):req('/pids-release?phase='+str(phase),{},base='http://target-front:9081',control_header=True)
def wait_rows(condition):
 end=time.monotonic()+15
 while time.monotonic()<end:
  rows=stats()
  if condition(rows):return rows
  time.sleep(.1)
 raise RuntimeError('owned_calls_did_not_overlap')
try:
 # Both heavy phase-one calls are held until explicitly released. The target
 # lets the innocent call return only after both have arrived. Its phase-two
 # arrival proves it completed while the other two calls still held their tasks.
 rows=wait_rows(lambda rows:len([r for r in rows.values()if r['phase']=='1'])==3 and any(r['phase']=='2'for r in rows.values()))
 check(req('/api/product/health').get('ready') is True, 'engine_health_under_three_calls')
 print(json.dumps({'innocent_completed_while_two_heavy_held':True}),flush=True)
 release(1)
 rows=wait_rows(lambda rows:len([r for r in rows.values()if r['phase']=='2'])==3)
 check(len(rows)==6,'six_owned_calls_required')
 for row in rows.values():
  check(int(row['limit'])==a.expected_tasks,'actual_rlimit_mismatch')
  innocent=row['phase']=='1' and row['children']=='0'
  if innocent:continue
  if a.sample_children<0:
   check(row['blocked']=='1' and int(row['children'])>0,'individual_limit_not_reached')
  else:check(int(row['children'])==a.sample_children,'sample_child_count_mismatch')
 req('/api/product/health')
 print(json.dumps({'three_calls_at_individual_limit':a.sample_children<0,'rows':rows}),flush=True)
 release(2)
 end=time.monotonic()+30
 while time.monotonic()<end:
  job=req('/api/diagnoses/v2/drafts/'+ident+'/status')['draft']
  if job['state']in('completed','failed','cancelled'):break
  time.sleep(.2)
 check(job['state']=='completed' and job['drain_complete'],'round_did_not_close')
 run=req('/api/tasks/'+job['task_id']+'/diagnosis-run')['run']
 check(run['phase']=='closed' and run['drain_complete'] and not run['closure_failed'],'persisted_drain_incomplete')
 counts=run['execution']['counts']
 for key,value in {'calls':6,'attempts':6,'forward_attempted':6,'responses':6,'unknown':0,'transient':0,'unsupported':0}.items():
  check(counts[key]==value,'unexpected_'+key)
 check(run['execution']['audit_complete'],'audit_incomplete')
 print(json.dumps({'passed':True,'task':job['task_id'],'outcome':run['outcome'],'counts':counts,'expected_command_tasks':a.expected_tasks}),flush=True)
finally:
 release(1)
 release(2)
