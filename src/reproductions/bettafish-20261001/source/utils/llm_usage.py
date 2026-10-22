"""Per-attempt model usage. Never persist prompts, responses or credentials."""
import json
import os
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit
from contextvars import ContextVar

current_run_id = ContextVar('bettafish_run_id', default=None)


def _get(value, key, default=None):
    return value.get(key, default) if isinstance(value, dict) else getattr(value, key, default)


def _number(value):
    return value if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def _numeric_fields(value):
    """Retain provider-specific numeric usage fields, never arbitrary strings."""
    if hasattr(value, 'model_dump'):
        value = value.model_dump()
    elif hasattr(value, '__dict__'):
        value = vars(value)
    if not isinstance(value, dict):
        return {}
    result = {}
    for key, item in value.items():
        if _number(item) is not None:
            result[key] = item
        elif isinstance(item, dict) or hasattr(item, 'model_dump'):
            result[key] = _numeric_fields(item)
    return result


def _usage(value):
    prompt = _number(_get(value, 'prompt_tokens'))
    cached = _number(_get(value, 'prompt_cache_hit_tokens'))
    if cached is None:
        cached = _number(_get(_get(value, 'prompt_tokens_details'), 'cached_tokens'))
    missed = _number(_get(value, 'prompt_cache_miss_tokens'))
    if missed is None and prompt is not None and cached is not None:
        missed = max(0, prompt - cached)
    return {
        'input_tokens': prompt,
        'output_tokens': _number(_get(value, 'completion_tokens')),
        'total_tokens': _number(_get(value, 'total_tokens')),
        'cache_hit_tokens': cached,
        'cache_miss_tokens': missed,
        'reasoning_tokens': _number(_get(_get(value, 'completion_tokens_details'), 'reasoning_tokens')),
    }


def _write(row):
    # A separate atomic file per request avoids interleaved writes across processes.
    folder = Path(os.getenv('LLM_USAGE_DIR', 'logs/llm_usage'))
    folder.mkdir(parents=True, exist_ok=True)
    dest = folder / (row['call_id'] + '.json')
    tmp = dest.with_suffix('.tmp')
    tmp.write_text(json.dumps(row, ensure_ascii=False), encoding='utf-8')
    tmp.replace(dest)


def recorded_completion(client, agent, **kwargs):
    """Preserve the SDK response/stream while recording each externally retried attempt.

    Set BETTAFISH_RUN_ID for an isolated benchmark. Otherwise calls remain explicitly
    unassigned; timestamps are not sufficient to infer concurrent task ownership.
    """
    start = time.monotonic()
    row = {
        'call_id': uuid.uuid4().hex,
        'run_id': os.getenv('BETTAFISH_RUN_ID') or current_run_id.get() or None,
        'agent': agent, 'model': kwargs.get('model'),
        'provider_host': urlsplit(str(client.base_url)).hostname,
        'started_at': datetime.now(timezone.utc).isoformat(),
        'stream': bool(kwargs.get('stream')), 'status': 'started',
        'usage_present': False, **_usage(None),
    }
    _write(row)

    def finish(status, error=None):
        row.update(status=status, ended_at=datetime.now(timezone.utc).isoformat(),
                   elapsed_seconds=round(time.monotonic() - start, 6))
        if error is not None:
            # Exception messages and request bodies may contain secrets.
            row['error_type'] = type(error).__name__
            row['http_status'] = _number(getattr(error, 'status_code', None))
        _write(row)

    def capture(response):
        usage = _get(response, 'usage')
        if usage is not None:
            row.update(_usage(usage))
            row['usage_present'] = True
            row['provider_usage'] = _numeric_fields(usage)
            counts = [row[k] for k in ('input_tokens', 'output_tokens', 'total_tokens')]
            row['token_sum_matches'] = (counts[0] + counts[1] == counts[2]) if all(x is not None for x in counts) else None
        row['response_model'] = _get(response, 'model')

    if kwargs.get('stream'):
        kwargs['stream_options'] = {**kwargs.get('stream_options', {}), 'include_usage': True}
    try:
        response = client.chat.completions.create(**kwargs)
    except Exception as exc:
        finish('error', exc)
        raise
    if not kwargs.get('stream'):
        capture(response)
        finish('completed')
        return response

    def stream():
        status = 'cancelled'
        error = None
        try:
            for chunk in response:
                capture(chunk)
                yield chunk
            status = 'completed'
        except Exception as exc:
            status, error = 'error', exc
            raise
        finally:
            try:
                response.close()
            finally:
                finish(status, error)
    return stream()
