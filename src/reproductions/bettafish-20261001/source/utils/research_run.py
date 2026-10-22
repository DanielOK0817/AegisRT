"""Persist dispatch claims so reconnecting Streamlit cannot repeat a paid run."""
import json
import os
import uuid
import time
from pathlib import Path
from datetime import datetime, timezone
from functools import wraps
from utils.llm_usage import current_run_id


def claim_run(run_id, agent):
    try:
        run_id = str(uuid.UUID(str(run_id)))
    except (ValueError, TypeError, AttributeError):
        return False
    if agent not in ('insight', 'media', 'query'):
        raise ValueError('Unknown agent')
    root = Path(os.getenv('RESEARCH_RUN_DIR', 'logs/research_runs'))
    root.mkdir(parents=True, exist_ok=True)
    path = root / f'{run_id}-{agent}.json'
    try:
        with path.open('x', encoding='utf-8') as file:
            json.dump({'run_id':run_id,'agent':agent,'claimed_at':datetime.now(timezone.utc).isoformat()},file)
    except FileExistsError:
        return False
    return True


def streamlit_run_id(st):
    params = st.query_params if hasattr(st, 'query_params') else st.experimental_get_query_params()
    value = params.get('run_id')
    return (value[0] if value else None) if isinstance(value, list) else value


def with_research_run(fn):
    @wraps(fn)
    def wrapped(*args, **kwargs):
        import streamlit as st
        token=current_run_id.set(streamlit_run_id(st) or None)
        run_id = os.getenv('BETTAFISH_RUN_ID') or current_run_id.get()
        agent = Path(fn.__code__.co_filename).name.split('_engine_')[0]
        root = Path(os.getenv('RESEARCH_RUN_DIR', 'logs/research_runs'))
        root.mkdir(parents=True, exist_ok=True)
        path = root / f'{run_id}-{agent}-timing.json'
        row = {'run_id':run_id, 'agent':agent, 'status':'running',
               'started_at':datetime.now(timezone.utc).isoformat()}
        start = time.monotonic()
        def save():
            tmp = path.with_suffix('.tmp')
            tmp.write_text(json.dumps(row), encoding='utf-8')
            tmp.replace(path)
        save()
        try:
            result = fn(*args, **kwargs)
            row['status'] = 'completed' if result is True else 'error' if result is False else 'finished'
            return result
        except BaseException:
            row['status'] = 'error'
            raise
        finally:
            current_run_id.reset(token)
            row.update(ended_at=datetime.now(timezone.utc).isoformat(), elapsed_seconds=round(time.monotonic()-start,3))
            save()
    return wrapped
