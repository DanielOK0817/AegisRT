"""Small paid connectivity probes; no credentials or response bodies are printed."""
import json
import os
from concurrent.futures import ThreadPoolExecutor
from openai import OpenAI
from config import settings
from utils.llm_usage import recorded_completion

PREFIXES = ['INSIGHT_ENGINE','MEDIA_ENGINE','QUERY_ENGINE','REPORT_ENGINE','FORUM_HOST']


def probe(prefix):
    key=getattr(settings,prefix+'_API_KEY')
    model=getattr(settings,prefix+'_MODEL_NAME')
    base=getattr(settings,prefix+'_BASE_URL')
    if not key:
        return {'agent':prefix,'model':model,'ok':False,'reason':'key_missing'}
    try:
        with OpenAI(api_key=key,base_url=base,max_retries=0,timeout=60) as client:
            chunks=recorded_completion(client,prefix.lower(),model=model,
                messages=[{'role':'user','content':'Reply with only the word OK.'}],
                max_tokens=1024,stream=True)
            parts=[]
            for chunk in chunks:
                if chunk.choices:
                    parts.append(chunk.choices[0].delta.content or '')
        answer=''.join(parts).strip()
        return {'agent':prefix,'model':model,'ok':bool(answer),'response_chars':len(answer)}
    except Exception as exc:
        status=getattr(exc,'status_code',None)
        body=getattr(exc,'body',None)
        error=body.get('error',body) if isinstance(body,dict) else {}
        code=error.get('code') if isinstance(error,dict) else None
        return {'agent':prefix,'model':model,'ok':False,'error_type':type(exc).__name__,
                'http_status':status,'provider_code':code if isinstance(code,(str,int)) else None}


if __name__=='__main__':
    os.environ.setdefault('BETTAFISH_RUN_ID','relayrouter-connectivity')
    with ThreadPoolExecutor(max_workers=5) as pool:
        rows=list(pool.map(probe,PREFIXES))
    print(json.dumps(rows,ensure_ascii=False))
