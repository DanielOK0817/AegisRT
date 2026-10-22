"""Verify evidence archives and independently check the submitted application case."""
from pathlib import Path
import hashlib,json,zipfile
ROOT=Path(__file__).resolve().parents[1]
KINDS={'asian-games':'asian-games','scheduler':'innovation/scheduler','context':'innovation/context','completion':'innovation/completion'}
def sha(data):return hashlib.sha256(data).hexdigest()
def verify_archive(folder):
 receipt=json.loads((folder/'receipt.json').read_text(encoding='utf-8'))
 archive=folder/receipt['archive'];assert sha(archive.read_bytes())==receipt['sha256'],str(folder)
 with zipfile.ZipFile(archive) as z:
  manifest=json.loads(z.read('bundle-manifest.json'));names=set()
  assert len(z.namelist())==len(set(z.namelist())),'duplicate archive member'
  for r in manifest['files']:
   assert r['path'] not in names,'duplicate archive entry';names.add(r['path'])
   assert sha(z.read(r['path']))==r['sha256'],r['path']
  assert names==set(z.namelist())-{'bundle-manifest.json'}
 return {'files':len(names),'anonymized_files':receipt['anonymized_files'],'sha256':receipt['sha256']}
def main():
 checked={k:verify_archive(ROOT/'tests'/v) for k,v in KINDS.items()}
 with zipfile.ZipFile(ROOT/'tests/asian-games/evidence.zip') as z:
  read=lambda n:json.loads(z.read(n))
  original=read('run-validation.json')
  for row in original['files']:assert sha(z.read('original/run/'+row['path']))==row['sha256'],row['path']
  snapshot=read('original/run/task-snapshot.json')
  assert len(snapshot['tasks'])==9
  assert sum(t['status']=='SUCCEEDED' for t in snapshot['tasks'])==original['tasks_completed']==8
  assert original['status']=='FAILED' and original['model_requests']['attempts']==46
  for name,row in read('recovery/export.json').items():assert sha(z.read('recovery/report/'+name))==row['sha256'],name
  verification=read('recovery/verification.json');latest=read('recovery/latest.json')
  assert verification['status']=='passed'
  assert verification['fresh_from_zero'] is False and verification['reused_tasks']==8
  assert latest['view']['status']=='SUCCEEDED' and latest['view']['original_status']=='FAILED'
  assert latest['summary']['model_requests']==verification['model_requests']==2
  assert all(v==0 for v in latest['summary']['runtime']['stats']['allocated'].values())
  assert latest['summary']['cleanup']['daemon_stopped'] and latest['summary']['cleanup']['tasks_terminal']
  report=z.read('recovery/report/report.html');assert sha(report)==verification['report_sha256']
  assert (ROOT/'demo/asian-games/report.html').read_bytes()==report
  content=read('recovery/report/report-content.json')
  assert len(content['sections'])==verification['sections']==7
  assert sum(len(s['paragraphs']) for s in content['sections'])==verification['paragraphs']==15
  assert (ROOT/'demo/asian-games/sample.json').read_bytes()==z.read('sample.json')
  assert '亚运会' in read('sample.json')['topic']
  observations=[json.loads(line) for line in z.read('observations.jsonl').splitlines() if line]
  assert all(o['status']['workflow_id']==original['workflow_id'] for o in observations)
  point=next(o for o in observations if o['time_s']==194.507)
  tasks=point['monitor']['tasks'];assert len(tasks)==9 and sum(len(t['dependencies']) for t in tasks)==16
  assert sum(t['status']=='RUNNING' for t in tasks)==3
  assert point['monitor']['host']['cpu_percent']==58.1 and point['monitor']['host']['memory_used_bytes']==1559998464
  query=next(t for t in tasks if t['stage']=='query-feedback')
  assert query['context']['shared_page_count']==2 and query['context']['logical_tokens']==662
  assert query['measurement']['memory_rss_bytes']==91660288 and query['measurement']['cpu_percent']==1.0
  forum=next(t for t in tasks if t['role']=='forum')
  assert forum['context']['shared_page_count']==4 and forum['context']['logical_tokens']==1499
  assert json.loads((ROOT/'demo/asian-games/observation.json').read_text(encoding='utf-8'))==point
 print(json.dumps({'passed':True,'archives':checked,'asian_games':{'observations':len(observations),'tasks':9,'dependencies':16,'report_sections':7,'report_paragraphs':15,'report_reused_tasks':8,'report_stage_model_requests':2,'fresh_from_zero':False,'resources_released':True},'new_model_calls':0},ensure_ascii=False))
if __name__=='__main__':main()
