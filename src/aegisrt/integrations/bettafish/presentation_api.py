"""Read bounded, identity-checked presentation output without changing execution."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import re

from flask import jsonify, send_from_directory
from runtime_monitor import RuntimeMonitor

UI = Path(__file__).resolve().parent / 'presentation'
MAX_BYTES = 8_000_000
MAX_TEXT = 18_000


def entry_page():
    return send_from_directory(UI, 'index.html')


def _read(path, parent):
    resolved = path.resolve()
    if resolved.parent != parent.resolve() or not resolved.is_file():
        return None
    with resolved.open('rb') as stream:
        raw = stream.read(MAX_BYTES + 1)
    if len(raw) > MAX_BYTES:
        return None
    return raw


def _json(path, parent):
    raw = _read(path, parent)
    if raw is None:
        return None
    try:
        result = json.loads(raw.decode('utf-8'))
        return result if isinstance(result, dict) else None
    except (ValueError, UnicodeError):
        return None


def _redactor(env_file):
    # Load locally only; no config or credential values are returned to the UI.
    secrets = []
    try:
        for line in env_file.read_text(encoding='utf-8-sig').splitlines():
            key, sep, value = line.partition('=')
            if sep and re.search(r'KEY|TOKEN|SECRET|PASSWORD', key, re.I):
                value = value.strip().strip('"\'')
                if len(value) >= 6:
                    secrets.append(value)
    except OSError:
        pass

    def redact(value, limit=MAX_TEXT):
        if not isinstance(value, str):
            return ''
        for secret in sorted(secrets, key=len, reverse=True):
            value = value.replace(secret, '[redacted]')
        value = re.sub(r'(?i)Bearer\s+[A-Za-z0-9_.-]+', 'Bearer [redacted]', value)
        value = re.sub(r'(?i)([?&](?:api[_-]?key|token|access_token|secret)=)[^\s&#]+', r'\1[redacted]', value)
        return value[:limit]
    return redact


def _task_failure(directory, workflow_id, task):
    """Expose only classified failure metadata, never exception text or logs."""
    state = _json(directory / 'state.json', directory)
    identity = (workflow_id, task.get('task_id', workflow_id + '-' + task['role']), task['role'].split('-')[0], 'live')
    if not state or tuple(state.get(key) for key in ('workflow_id', 'task_id', 'role', 'mode')) != identity:
        return None
    if state.get('status') != 'failed':
        return None
    reason = state.get('error_reason')
    usage = state.get('model_requests', {})
    code, message = 'task_failed', '任务执行失败，已保存的研究内容保留。'
    if reason == 'model request budget exhausted':
        code, message = 'model_budget_exhausted', '模型请求额度已用尽，后续步骤未能完成。'
    elif reason == 'model request budget exhausted: capacity reserved for required research steps':
        code, message = 'research_capacity_insufficient', '必要研究步骤已预留额度，剩余恢复容量不足，任务及后续步骤已停止。'
    elif reason in ('provider request failed', 'provider stream failed'):
        code, message = 'provider_failed', '模型服务请求未完成，任务已停止。'
        attempts = usage.get('provider_attempts') if isinstance(usage, dict) else None
        last = attempts[-1] if isinstance(attempts, list) and attempts else None
        if (isinstance(last, dict) and last.get('retry_blocked_reason') == 'reserved_for_required_research_calls'
                and last.get('status') == 'error' and last.get('agent') == identity[2]
                and last.get('retryable') is True and last.get('delivered_chunks') == 0
                and last.get('failure_stage') in ('request', 'stream')
                and all(type(usage.get(key)) is int and usage[key] >= 0 for key in ('attempts', 'limit'))
                and type(last.get('attempt')) is int and last['attempt'] == usage['attempts']
                and all(type(last.get(key)) is int and last[key] >= 0 for key in ('remaining_capacity', 'required_calls_after'))
                and last['remaining_capacity'] == usage['limit'] - usage['attempts']
                and 0 < last['remaining_capacity'] <= last['required_calls_after']):
            code, message = 'recovery_capacity_insufficient', '模型服务请求未完成，剩余恢复容量不足，任务及后续步骤已停止。'
    elif reason in ('model response did not finish normally', 'model stream did not finish normally', 'model stream ended without a completion marker'):
        code, message = 'incomplete_response', '模型响应未完整结束，任务已停止。'
    failure = {'stage': task['role'], 'role': identity[2], 'name': task['name'], 'code': code, 'message': message}
    if isinstance(usage, dict) and all(type(usage.get(key)) is int and usage[key] >= 0 for key in ('attempts', 'limit')):
        failure['model_requests'] = {key: usage[key] for key in ('attempts', 'limit')}
    return failure


def workflow_content(service, workflow_id):
    folder = service.job_dir(workflow_id)
    status = service.status(workflow_id)
    redact = _redactor(service.env_file)
    blocks, unavailable, failures = [], [], []
    for task in status['tasks']:
        stage = task['role']
        role = stage.split('-')[0]
        task_id = task.get('task_id', workflow_id + '-' + stage)
        directory = (folder / task_id).resolve()
        if directory.parent != folder:
            unavailable.append(stage)
            continue
        try:
            if task['status'] == 'FAILED':
                failure = _task_failure(directory, workflow_id, task)
                if failure:
                    failures.append(failure)
            if role in ('query', 'media', 'insight'):
                checkpoint = _json(directory / 'checkpoint.json', directory)
                if not checkpoint:
                    continue
                if (checkpoint.get('workflow_id'), checkpoint.get('task_id'), checkpoint.get('role'), checkpoint.get('mode')) != (workflow_id, task_id, role, 'live'):
                    unavailable.append(stage)
                    continue
                state = checkpoint.get('state', {})
                paragraphs = state.get('paragraphs', []) if isinstance(state, dict) else []
                if not isinstance(paragraphs, list):
                    continue
                for i, paragraph in enumerate(paragraphs[:12]):
                    if not isinstance(paragraph, dict):
                        continue
                    research = paragraph.get('research', {})
                    text = research.get('latest_summary', '') if isinstance(research, dict) else ''
                    if not isinstance(text, str) or not text.strip():
                        continue
                    blocks.append({'id': f'{stage}:{i}', 'stage': stage, 'role': role,
                                   'title': redact(paragraph.get('title', ''), 200) or f'研究段落{i+1}',
                                   'text': redact(text), 'truncated': len(text) > MAX_TEXT,
                                   'status': task['status'], 'source': 'research_checkpoint'})
            elif role == 'forum':
                manifest = _json(directory / 'result.json', directory)
                if not manifest:
                    continue
                if (manifest.get('schema'), manifest.get('workflow_id'), manifest.get('task_id'), manifest.get('role'), manifest.get('mode')) != ('bettafish.task-result/v1', workflow_id, task_id, 'forum', 'live'):
                    unavailable.append(stage)
                    continue
                record = manifest.get('files', {}).get('report', {})
                path = Path(record.get('path', ''))
                if path.name != 'forum.md' or path.resolve().parent != directory:
                    unavailable.append(stage)
                    continue
                raw = _read(path, directory)
                if raw is None or hashlib.sha256(raw).hexdigest() != record.get('sha256'):
                    unavailable.append(stage)
                    continue
                text = raw.decode('utf-8')
                blocks.append({'id': stage, 'stage': stage, 'role': role, 'title': task['name'],
                               'text': redact(text), 'truncated': len(text) > MAX_TEXT,
                               'status': task['status'], 'source': 'verified_forum_artifact'})
            # Report bodies stay behind service.report_path's final integrity gate.
        except (OSError, ValueError, TypeError, AttributeError, KeyError):
            unavailable.append(stage)
    return {'success': True, 'workflow_id': workflow_id, 'blocks': blocks,
            'unavailable_stages': unavailable, 'task_failures': failures, 'delivery': 'saved_paragraphs',
            'report_url': status.get('report_url')}


def install_presentation(blueprint, service):
    monitor = RuntimeMonitor()

    @blueprint.get('/ui/')
    def presentation_page():
        return entry_page()

    @blueprint.get('/ui/<name>')
    def presentation_asset(name):
        if name not in ('runtime.js', 'runtime.css'):
            return jsonify(success=False, message='文件不存在'), 404
        return send_from_directory(UI, name)

    @blueprint.get('/workflows')
    def recent_workflows():
        rows = []
        for workflow_id in service.observed:
            status = service.status(workflow_id)
            rows.append({**{key: status[key] for key in ('workflow_id', 'query', 'status', 'sample_count')},
                         'task_count': len(status['tasks']), 'readonly': True})
        for path in sorted(service.run_root.glob('*/entry.json'), reverse=True):
            try:
                status = service.status(path.parent.name)
                rows.append({**{key: status[key] for key in ('workflow_id', 'query', 'status', 'sample_count')},
                             'task_count': len(status['tasks'])})
            except (OSError, ValueError, KeyError):
                continue
            if len(rows) == 15:
                break
        response = jsonify(success=True, workflows=rows)
        response.headers['Cache-Control'] = 'no-store'
        return response

    @blueprint.get('/workflows/<workflow_id>/content')
    def content(workflow_id):
        response = jsonify(workflow_content(service.reader(workflow_id), workflow_id))
        response.headers['Cache-Control'] = 'no-store'
        response.headers['X-Content-Type-Options'] = 'nosniff'
        return response

    @blueprint.get('/workflows/<workflow_id>/monitor')
    def workflow_monitor(workflow_id):
        response = jsonify(monitor.workflow(service.reader(workflow_id), workflow_id))
        response.headers['Cache-Control'] = 'no-store'
        response.headers['X-Content-Type-Options'] = 'nosniff'
        return response
