"""Run the exported adapter tests with no bytecode written into frozen source."""
from pathlib import Path
import os, subprocess, sys
ROOT=Path(__file__).resolve().parents[1]
repo=ROOT/'src/aegisrt'
env=dict(os.environ,PYTHONDONTWRITEBYTECODE='1',PYTHONIOENCODING='utf-8')
env['PYTHONPATH']=os.pathsep.join([str(repo/'integrations/bettafish'),str(repo/'integrations/bettafish/tests'),os.environ.get('PYTHONPATH','')])
for folder,pattern in [('integrations/bettafish','test_adapter.py'),('integrations/bettafish/tests','test_*.py')]:
    result=subprocess.run([sys.executable,'-B','-m','unittest','discover','-s',folder,'-p',pattern],cwd=repo,env=env)
    if result.returncode:sys.exit(result.returncode)
