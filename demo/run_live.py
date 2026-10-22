"""Prepare an isolated nine-task application workspace; --execute starts real model calls."""
from pathlib import Path
import argparse,json,os,shutil,subprocess,sys

ROOT=Path(__file__).resolve().parents[1]
def fs(path):
 value=str(path.resolve())
 return '\\\\?\\'+value if os.name=='nt' and not value.startswith('\\\\?\\') else value

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--output',type=Path,required=True,help='New directory outside the repository')
 parser.add_argument('--sample',type=Path,default=ROOT/'demo/asian-games/sample.json')
 parser.add_argument('--env-file',type=Path,help='External private model/search service configuration')
 parser.add_argument('--execute',action='store_true',help='Run nine tasks with up to 58 model requests')
 args=parser.parse_args();output=args.output.resolve()
 if output==ROOT or ROOT in output.parents or output.exists():parser.error('Output must be a new directory outside the repository')
 if args.execute and (not args.env_file or not args.env_file.is_file()):parser.error('--execute requires an existing external --env-file')
 if args.env_file and (ROOT==args.env_file.resolve() or ROOT in args.env_file.resolve().parents):parser.error('Keep private configuration outside the repository')
 sample=json.loads(args.sample.read_text(encoding='utf-8-sig'))
 output.mkdir(parents=True)
 shutil.copytree(fs(ROOT/'src'),fs(output/'src'),ignore=shutil.ignore_patterns('__pycache__','*.pyc'))
 integration=output/'src/aegisrt/integrations/bettafish'
 sys.path.insert(0,str(integration))
 from sample_inputs import freeze_sample_values
 frozen=freeze_sample_values(sample['topic'],sample['comments'],sample['provenance'],output/'inputs')
 binary=output/('aegisrt.exe' if os.name=='nt' else 'aegisrt')
 env=dict(os.environ,PYTHONDONTWRITEBYTECODE='1',PYTHONIOENCODING='utf-8')
 command=[sys.executable,'-B',str(integration/'runner.py'),'--mode','live',
          '--aegisrt',str(binary),'--input-dir',frozen['input_dir'],
          '--source-dir',str(output/'src/reproductions/bettafish-20261001/source'),
          '--run-dir',str(output/'runs'),'--topic',sample['topic'],'--fixed-topic-search',
          '--max-paragraphs','2','--max-reflections','1','--insight-max-reflections','1',
          '--model-request-limits','14,11,11,1,5','--refinement-request-limits','7,4,4']
 if args.env_file:command+=['--env-file',str(args.env_file.resolve())]
 (output/'launch-plan.json').write_text(json.dumps({'mode':'live','task_count':9,'max_model_requests':58,
    'execute_requested':args.execute,'command':command},ensure_ascii=False,indent=2),encoding='utf-8')
 subprocess.run(['go','build','-trimpath','-buildvcs=false','-mod=readonly','-o',str(binary),'./cmd/aegisrt'],cwd=output/'src/aegisrt',env=env,check=True)
 if args.execute:subprocess.run(command,cwd=output,env=env,check=True)
 else:print(json.dumps({'prepared':True,'task_count':9,'model_calls':0,'output':str(output)},ensure_ascii=False))

if __name__=='__main__':main()
