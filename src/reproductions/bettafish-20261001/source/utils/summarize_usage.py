"""Aggregate recorded usage without replacing missing values with invented totals."""
import argparse
import json
from collections import defaultdict
from pathlib import Path


def summarize(folder, run_id=None):
    groups = defaultdict(list)
    for path in Path(folder).glob('*.json'):
        row = json.loads(path.read_text(encoding='utf-8'))
        if run_id is None or row.get('run_id') == run_id:
            groups[(row.get('run_id'),row['agent'],row['model'])].append(row)
    results = []
    for (run,agent,model), rows in sorted(groups.items(), key=lambda x: str(x[0])):
        result = {'run_id':run, 'agent':agent, 'model':model, 'attempts':len(rows),
                  'completed':sum(r['status']=='completed' for r in rows),
                  'missing_usage_attempts':sum(not r.get('usage_present') for r in rows),
                  'token_sum_mismatch_attempts':sum(r.get('token_sum_matches') is False for r in rows),
                  'request_seconds_sum':round(sum(r.get('elapsed_seconds',0) for r in rows),3)}
        for field in ('input_tokens','output_tokens','total_tokens','cache_hit_tokens','cache_miss_tokens','reasoning_tokens'):
            known = [r[field] for r in rows if r.get(field) is not None]
            result[field] = {'reported_sum':sum(known) if known else None,
                             'known_attempts':len(known), 'complete':len(known)==len(rows)}
        results.append(result)
    return results


if __name__ == '__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--directory',default='logs/llm_usage')
    parser.add_argument('--run-id')
    args=parser.parse_args()
    print(json.dumps(summarize(args.directory,args.run_id),ensure_ascii=False,indent=2))
