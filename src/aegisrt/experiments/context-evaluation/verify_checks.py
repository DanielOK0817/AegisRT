"""Negative controls for the independent raw-data verifier."""
import argparse
import copy
from pathlib import Path
import tempfile
import analyze as a


def main():
    p=argparse.ArgumentParser();p.add_argument("run",type=Path);args=p.parse_args()
    meta=a.read(args.run/"run.json")
    comm=next((x for x in meta["jobs"] if x["job"]["kind"]=="communication"),None)
    comp=next(x for x in meta["jobs"] if x["job"]["kind"]=="compression" and x["job"]["mode"]=="cache" and x["job"]["fraction_eighths"]<8)
    rejected=[]
    for name,entry in [("latency_quantile",comm),("delivery_bytes",comm),("output_body",comp),("summary_calls",comp),("retained_facts",comp)]:
        if entry is None:continue
        directory=args.run/("trial-"+entry["id"])
        result=copy.deepcopy(a.read(directory/"result.json")["result"])
        if name=="latency_quantile":result["p50_ns"]+=1
        elif name=="delivery_bytes":result["samples"][0]["delivered_bytes"]+=1
        elif name=="output_body":result["samples"][0]["text"]+=" invented"
        elif name=="summary_calls":result["samples"][1]["summary_calls"]+=1
        elif name=="retained_facts":result["samples"][0]["facts_retained"][0]=not result["samples"][0]["facts_retained"][0]
        try:
            if entry["job"]["kind"]=="communication":a.verify_communication(result,entry["job"],entry["variant"],directory)
            else:a.verify_compression(result,entry["job"],directory)
        except ValueError as error:rejected.append({"mutation":name,"rejected":True,"reason":str(error)})
        else:raise RuntimeError("verifier accepted mutation "+name)
    with tempfile.TemporaryDirectory(prefix="aegis-evaluation-negative-") as temporary:
        envelope=a.read(args.run/("trial-"+comp["id"])/"context.state")
        envelope["checksum"]="0"*64
        target=Path(temporary)/"tampered.state";a.write(target,envelope)
        try:a.checkpoint(target)
        except ValueError:rejected.append({"mutation":"checkpoint_checksum","rejected":True})
        else:raise RuntimeError("accepted bad checkpoint")
    a.write(args.run/"analysis"/"negative-controls.json",rejected)
    print("Rejected all",len(rejected),"mutations")


if __name__=="__main__":main()
