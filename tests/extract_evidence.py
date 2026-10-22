"""Extract a verified evidence bundle into a new external directory."""
from pathlib import Path,PurePosixPath
import argparse,os,zipfile
from verify_evidence import ROOT,KINDS,verify_archive
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--kind',choices=KINDS,required=True);p.add_argument('--output',type=Path,required=True)
 a=p.parse_args();output=a.output.resolve()
 if output.exists() or output==ROOT or ROOT in output.parents:p.error('Use a new directory outside the submission')
 source=ROOT/'tests'/KINDS[a.kind];verify_archive(source)
 with zipfile.ZipFile(source/'evidence.zip') as z:
  for name in z.namelist():
   path=PurePosixPath(name)
   if path.is_absolute() or '..' in path.parts or '\\' in name or ':' in name:raise ValueError('Unsafe archive path')
  output.mkdir(parents=True)
  for name in z.namelist():
   target=output/name
   if os.name=='nt':target=Path('\\\\?\\'+str(target))
   target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(z.read(name))
 print(str(output))
if __name__=='__main__':main()
