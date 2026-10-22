"""One explicitly authorized report recovery, retaining the failed run unchanged."""
from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
from pathlib import Path
import secrets
import shutil
import subprocess
import time

from runner import Client, digest, save_json, unused_port, utc_now, stop_owned_daemon, TERMINAL

DIRECTORY = '.report-recovery-1'
SUFFIX = '-report-recovery-1'
NOTICE = '本报告仅恢复了报告任务，复用本轮八项已完成的研究与讨论结果；原报告失败记录保留。'
STAGES = ('query', 'media', 'insight', 'forum', 'query-feedback', 'media-feedback', 'insight-feedback', 'forum-final')


def read(path):
    return json.loads(Path(path).read_text(encoding='utf-8'))


def folder_for(root, name=None):
    root = Path(root).resolve()
    pointer = root/'report-recovery-index.json'
    if name is None:
        name = read(pointer)['directory'] if pointer.is_file() else DIRECTORY
    if not isinstance(name,str) or not re.fullmatch(r'\.report-recovery-[1-9][0-9]?',name):
        raise ValueError('Invalid report recovery directory')
    folder = root/name
    if folder.resolve().parent != root:
        raise ValueError('Recovery directory escapes original run')
    return folder


def recovery_notice(page):
    return page.replace('<main>', '<main><p class="notice">' + NOTICE + '</p>', 1)


def runtime_command(binary, control, port):
    # Retain the original scheduler capacity and worker configuration.
    return [str(Path(binary).resolve()), '-listen', f'127.0.0.1:{port}',
            '-agents-file', str(control / 'agents.json'), '-auth-token-file', str(control / 'auth.token'),
            '-state-file', str(control / 'runtime.state'), '-max-workers', '3', '-poll-interval', '50ms',
            '-cpu-milli', '4000', '-memory-mib', '4096', '-llm-slots', '3', '-token-budget', '64000',
            '-context-counter', 'unicode-segment-v1']


def rebind_configuration(envelope, fingerprint):
    """Migrate only configuration identity on a copy, retaining all runtime data."""
    raw = base64.b64decode(envelope['payload'], validate=True)
    if hashlib.sha256(raw).hexdigest() != envelope['checksum']:
        raise ValueError('Original checkpoint checksum differs')
    if len(fingerprint) != 64 or any(c not in '0123456789abcdef' for c in fingerprint):
        raise ValueError('Invalid new configuration fingerprint')
    payload = json.loads(raw)
    before = payload['config_fingerprint']
    # Replace only the exact JSON string; preserve every other payload byte.
    old = json.dumps(before).encode()
    if raw.count(old) != 1:
        raise ValueError('Ambiguous checkpoint configuration identity')
    changed = raw.replace(old, json.dumps(fingerprint).encode(), 1)
    check = json.loads(changed)
    if check != {**payload, 'config_fingerprint': fingerprint}:
        raise ValueError('Migration changed runtime data')
    return {**envelope, 'payload': base64.b64encode(changed).decode(), 'checksum': hashlib.sha256(changed).hexdigest()}


def migrate_configuration(root, binary, name=None):
    """Ask the actual runtime for its new identity; no tasks or models execute."""
    root = Path(root)
    folder = folder_for(root,name)
    control = folder/'.control'
    probe = folder / 'configuration-probe'
    probe.mkdir(exist_ok=False)
    shutil.copy2(control / 'agents.json', probe / 'agents.json')
    token = secrets.token_urlsafe(32)
    (probe / 'auth.token').write_text(token, encoding='ascii')
    (probe / 'auth.token').chmod(0o600)
    port = unused_port()
    client, proc = Client(port, token), None
    cleanup = {'error_types': []}
    try:
        with (probe / 'daemon.log').open('ab') as log:
            proc = subprocess.Popen(runtime_command(binary, probe, port), stdout=log, stderr=log, cwd=root)
        for _ in range(150):
            if proc.poll() is not None:
                raise RuntimeError('Configuration probe exited')
            try:
                if client.call('/readyz')['status'] == 'ready':
                    break
            except (OSError, RuntimeError):
                pass
            time.sleep(.2)
        else:
            raise TimeoutError('Configuration probe readiness timeout')
        if client.call('/v1/tasks')['tasks']:
            raise ValueError('Configuration probe unexpectedly has tasks')
    finally:
        stop_owned_daemon(proc, cleanup)
    source = read(root / '.control/runtime.state')
    fresh = json.loads(base64.b64decode(read(probe / 'runtime.state')['payload']))
    migrated = rebind_configuration(source, fresh['config_fingerprint'])
    target = control / 'runtime.state'
    before_hash = digest(target)
    save_json(target, migrated)
    target.chmod(0o600)
    if digest(control / 'runtime.state.context') != digest(root / '.control/runtime.state.context'):
        raise ValueError('Migration changed original context')
    receipt = {'schema': 'bettafish.recovery-configuration-migration/v1',
               'source_sha256': digest(root / '.control/runtime.state'), 'copy_before_sha256': before_hash,
               'copy_after_sha256': digest(target), 'new_config_fingerprint': fresh['config_fingerprint'],
               'only_payload_field_changed': 'config_fingerprint', 'context_bytes_unchanged': True,
               'original_tasks_unchanged': True, 'probe_tasks': 0, 'model_calls': 0, 'cleanup': cleanup}
    save_json(folder / 'configuration-migration.json', receipt)
    return receipt


def original_states(root):
    wf = root.name
    rows = read(root / 'task-snapshot.json')['tasks']
    states = {r['task']['id']: r['status'] for r in rows if r['task']['workflow_id'] == wf}
    expected = {wf + '-' + stage: 'SUCCEEDED' for stage in STAGES}
    expected[wf + '-report'] = 'FAILED'
    if len(rows) != 9 or states != expected:
        raise ValueError('Recovery requires exactly eight completed tasks and one failed report')
    return states


def record(root, name=None):
    """An operator-created, fixed-location record; never selected by HTTP input."""
    root = Path(root).resolve()
    folder = folder_for(root,name)
    if folder.resolve().parent != root:
        raise ValueError('Recovery directory escapes original run')
    path = folder / 'authorization.json'
    value = read(path)
    if (value.get('schema') != 'bettafish.report-recovery/v1' or value.get('workflow_id') != root.name
            or value.get('task_id') != root.name + '-' + folder.name.lstrip('.') or value.get('original_task_id') != root.name + '-report'
            or value.get('max_model_requests') != 5 or type(value.get('prior_aggregate_requests')) is not int
            or not 0 <= value['prior_aggregate_requests'] <= 591
            or value.get('aggregate_limit') != value['prior_aggregate_requests']+5
            or value.get('original_statuses') != original_states(root)):
        raise ValueError('Recovery authorization or original terminal states differ')
    return value


def check_originals(root, auth):
    root = Path(root).resolve()
    for name, expected in auth['original_sha256'].items():
        path = (root / name).resolve(strict=True)
        if not path.is_relative_to(root) or digest(path) != expected:
            raise ValueError('Original run evidence changed')


def check_code(auth):
    directory = Path(auth['integration_dir']).resolve(strict=True)
    for name, expected in auth['code_sha256'].items():
        path = (directory / name).resolve(strict=True)
        if path.parent != directory or digest(path) != expected:
            raise ValueError('Recovery code changed after authorization')


def validate_adapter_recovery(args, request, payload):
    root = Path(args.run_dir).resolve() / request['workflow_id']
    auth = record(root)
    if auth.get('execution_authorized') is False:
        raise ValueError('Report recovery has not been explicitly authorized to execute')
    expected_seed = auth.get('correction_seed', {}).get('source_task_id')
    if getattr(args, 'report_correction_from', None) != expected_seed:
        raise ValueError('Report correction source differs from authorization')
    if (args.role != 'report' or args.mode != 'live' or getattr(args, 'resume_dir', None)
            or args.recovery_of != auth['original_task_id'] or request['task_id'] != auth['task_id']
            or payload['max_model_requests'] != 5):
        raise ValueError('Only the separately authorized report task can recover')
    check_originals(root, auth)
    check_code(auth)
    return {'original_task_id': auth['original_task_id'], 'reused_tasks': 8,
            'authorization_sha256': digest(folder_for(root) / 'authorization.json')}


def prepare(root, launch_file, *, name=DIRECTORY, prior_aggregate=584, correction_from=None):
    root = Path(root).resolve(strict=True)
    launch = read(launch_file)
    if launch['workflow_id'] != root.name or launch['aggregate_limit'] != 596:
        raise ValueError('Original authorized launch differs')
    original = original_states(root)
    summary = read(root / 'summary.json')
    if summary['status'] != 'error' or not summary['cleanup']['daemon_stopped'] or not summary['cleanup']['tasks_terminal']:
        raise ValueError('Original run must be stopped before recovery')
    state = read(root / (root.name + '-report') / 'state.json')
    if state['status'] != 'failed':
        raise ValueError('Original report did not fail')
    if type(prior_aggregate) is not int or not 0 <= prior_aggregate <= 591:
        raise ValueError('Recovery would exceed the previously approved aggregate ceiling')
    folder = folder_for(root,name)
    folder.mkdir(exist_ok=False)
    source_hashes = {p.relative_to(root).as_posix(): digest(p) for p in root.rglob('*')
                     if p.is_file() and folder not in p.parents and p.name not in ('auth.token','report-recovery-index.json') and not p.name.endswith('.lock')}
    integration = Path(__file__).resolve().parent
    auth = {'schema': 'bettafish.report-recovery/v1', 'workflow_id': root.name,
            'task_id': root.name + '-' + folder.name.lstrip('.'), 'original_task_id': root.name + '-report',
            'max_model_requests': 5, 'prior_aggregate_requests': prior_aggregate, 'aggregate_limit': prior_aggregate+5,
            'original_statuses': original, 'original_sha256': source_hashes,
            'integration_dir': str(integration), 'code_sha256': {p.name: digest(p) for p in integration.glob('*.py')},
            'launch_file': str(Path(launch_file).resolve()), 'launch_sha256': digest(launch_file),
            'created_at': utc_now(), 'execution_authorized': False,
            'authorization': 'Prepared report-only recovery. A separate explicit operator approval is required before starting.'}
    if correction_from:
        from report_seed import load_seed
        auth['correction_seed'] = load_seed(root,correction_from,launch['topic'],auth['task_id'])
    control = folder / '.control'
    control.mkdir()
    for filename in ('runtime.state', 'runtime.state.context'):
        shutil.copy2(root / '.control' / filename, control / filename)
        if os.name != 'nt':
            (control / filename).chmod(0o600)
    registry = read(root / '.control/agents.json')
    for agent in registry['agents']:
        if agent['id'] == 'bettafish-report':
            agent['args'][0] = str(integration / 'adapter.py')
            agent['args'] += ['--recovery-of', auth['original_task_id']]
            if correction_from:
                agent['args'] += ['--report-correction-from', correction_from]
    save_json(control / 'agents.json', registry)
    task = next(row for row in read(root / 'submitted-tasks.json')['tasks'] if row['id'] == auth['original_task_id'])
    task.update(id=auth['task_id'], idempotency_key=auth['task_id'], max_attempts=1)
    goal = json.loads(task['goal'])
    if goal['max_model_requests'] != 5 or goal['model_timeout_seconds'] != 300 or goal['max_output_tokens'] != 32000:
        raise ValueError('Report recovery must retain tested budget and output capacity')
    save_json(folder / 'submitted-tasks.json', {'tasks': [task]})
    migrate_configuration(root, '/work/bin/aegisrt', folder.name)
    auth['configuration_migration_sha256'] = digest(folder / 'configuration-migration.json')
    save_json(folder / 'authorization.json', auth)
    return {'status': 'prepared', 'workflow_id': root.name, 'task_id': auth['task_id'], 'model_calls': 0}


def run(root, binary, *, name=None):
    root = Path(root).resolve(strict=True)
    auth = record(root,name)
    if auth.get('execution_authorized') is not True:
        raise ValueError('Report recovery has not been explicitly authorized to execute')
    check_originals(root, auth)
    check_code(auth)
    folder = folder_for(root,name)
    control = folder/'.control'
    # Exclusive lock consumes this one authorization even if startup fails.
    with (folder / 'start-once.lock').open('x'):
        pass
    save_json(root/'report-recovery-index.json', {'directory':folder.name})
    token = secrets.token_urlsafe(32)
    token_path = control / 'auth.token'
    token_path.write_text(token, encoding='ascii')
    token_path.chmod(0o600)
    port = unused_port()
    client = Client(port, token)
    command = runtime_command(binary, control, port)
    summary = {'workflow_id': root.name, 'task_id': auth['task_id'], 'status': 'starting',
               'started_at': utc_now(), 'reused_tasks': 8, 'fresh_from_zero': False}
    save_json(folder / 'summary.json', summary)
    pages, after, stream = [], '0', None
    def capture():
        nonlocal after, stream
        snapshot = client.call('/v1/tasks')
        save_json(folder / 'task-snapshot.json', snapshot)
        page = client.call(f'/v1/events?after_id={after}&limit=256' + (f'&stream_id={stream}' if stream else ''))
        if page.get('events'):
            pages.append(page)
        after, stream = page.get('next_after_id', after), page.get('stream_id', stream)
        save_json(folder / 'events.json', {'pages': pages})
        return snapshot
    proc, started = None, time.monotonic()
    cleanup = {'error_types': [], 'daemon_stopped': False, 'tasks_terminal': False}
    try:
        with (control / 'daemon.log').open('ab') as log:
            proc = subprocess.Popen(command, stdout=log, stderr=log, cwd=root)
        for _ in range(150):
            if proc.poll() is not None:
                raise RuntimeError('Recovery runtime exited before readiness')
            try:
                if client.call('/readyz')['status'] == 'ready':
                    break
            except (OSError, RuntimeError):
                pass
            time.sleep(.2)
        else:
            raise TimeoutError('Recovery runtime readiness timed out')
        restored = client.call('/v1/tasks')
        if {r['task']['id']: r['status'] for r in restored['tasks']} != auth['original_statuses']:
            raise ValueError('Restored state differs from original nine terminal tasks')
        client.call('/v1/task-graphs/patches', read(folder / 'submitted-tasks.json'))
        summary['status'] = 'running'
        save_json(folder / 'summary.json', summary)
        while time.monotonic() - started < 1800:
            rows = capture()['tasks']
            unchanged = {r['task']['id']: r['status'] for r in rows if r['task']['id'] != auth['task_id']}
            if unchanged != auth['original_statuses']:
                raise ValueError('Recovery changed an original task')
            row = next(r for r in rows if r['task']['id'] == auth['task_id'])
            if row['status'] in TERMINAL:
                if row['status'] != 'SUCCEEDED':
                    raise RuntimeError('Report recovery failed; no additional attempt authorized')
                summary['status'] = 'completed'
                summary['runtime'] = client.call('/v1/runtime')
                summary['output_summary'] = client.call('/v1/tasks/' + auth['task_id'] + '/output-summary')
                break
            time.sleep(1)
        else:
            raise TimeoutError('Report recovery exceeded its wall budget')
    except BaseException as exc:
        summary.update(status='error', error_type=type(exc).__name__, error=str(exc))
    finally:
        if proc is not None and proc.poll() is None:
            try:
                rows = client.call('/v1/tasks')['tasks']
                for row in rows:
                    if row['status'] not in TERMINAL:
                        client.call('/v1/tasks/' + row['task']['id'] + '/cancel', {})
                for _ in range(30):
                    snapshot = capture()
                    cleanup['tasks_terminal'] = all(r['status'] in TERMINAL for r in snapshot['tasks'])
                    if cleanup['tasks_terminal']:
                        break
                    time.sleep(.2)
            except Exception as exc:
                cleanup['error_types'].append(type(exc).__name__)
        stop_owned_daemon(proc, cleanup)
        check_originals(root, auth)
        budget_path = root / auth['task_id'] / 'model-budget.json'
        attempts = read(budget_path)['attempts'] if budget_path.exists() else 0
        summary.update(ended_at=utc_now(), elapsed_seconds=round(time.monotonic()-started, 3),
                       cleanup=cleanup, model_requests=attempts, aggregate_requests=auth['prior_aggregate_requests']+attempts)
        save_json(folder / 'summary.json', summary)
    return summary


def view(root):
    root = Path(root)
    if not (folder_for(root) / 'authorization.json').is_file():
        return None
    auth = record(root)
    folder = folder_for(root)
    interruption = read(folder / 'interruption.json') if (folder / 'interruption.json').is_file() else None
    if interruption and (interruption.get('workflow_id') != root.name or interruption.get('task_id') != auth['task_id']
            or interruption.get('reason') != 'execution_node_interrupted'
            or interruption.get('automatic_restart') is not False):
        raise ValueError('Unexpected recovery interruption record')
    summary = read(folder / 'summary.json') if (folder / 'summary.json').exists() else {}
    status = 'WAITING'
    if not interruption and (folder / 'task-snapshot.json').exists():
        rows = read(folder / 'task-snapshot.json')['tasks']
        row = next((r for r in rows if r['task']['id'] == auth['task_id'] and r['task']['workflow_id'] == root.name), None)
        if row:
            status = row['status']
    if summary.get('status') == 'error' and status not in ('FAILED', 'CANCELED'):
        status = 'FAILED'
    if interruption:
        status = 'FAILED'
    path = root / auth['task_id'] / 'model-budget.json'
    return {'task_id': auth['task_id'], 'original_task_id': auth['original_task_id'], 'status': status,
            'reused_tasks': 8, 'model_requests': read(path)['attempts'] if path.exists() else 0,
            'limit': 5, 'original_status': 'FAILED', 'fresh_from_zero': False,
            'interrupted': bool(interruption),
            'message': ('报告恢复因执行节点中断而停止，未自动重跑。' + NOTICE if interruption else NOTICE)}


def report_path(root):
    """Only independently verified and browser-checked, byte-bound recovery output."""
    root = Path(root).resolve(strict=True)
    auth = record(root)
    check_originals(root, auth)
    check_code(auth)
    folder = folder_for(root)
    verification = read(folder / 'verification.json')
    browser = read(folder / 'browser-qa.json')
    if (verification.get('status') != 'passed' or verification.get('task_id') != auth['task_id']
            or verification.get('authorization_sha256') != digest(folder / 'authorization.json')):
        raise ValueError('Independent recovery verification has not passed')
    if (browser.get('schema') != 'bettafish.report-recovery-browser-qa/v1' or browser.get('status') != 'passed'
            or browser.get('task_id') != auth['task_id'] or browser.get('verification_sha256') != digest(folder / 'verification.json')
            or browser.get('report_content_edited') is not False or browser.get('model_requests') != 0
            or not browser.get('cases') or any(c.get('status') != 'passed' for c in browser['cases'])
            or any(browser.get(k) != [] for k in ('console_errors', 'page_errors', 'blocked_requests'))):
        raise ValueError('Recovery browser verification has not passed')
    for name, expected in verification['sha256'].items():
        path = (root / name).resolve(strict=True)
        if not path.is_relative_to(root) or digest(path) != expected:
            raise ValueError('Recovered evidence changed after verification')
    path = (root / auth['task_id'] / 'report.html').resolve(strict=True)
    if path.parent.parent != root or digest(path) != browser.get('report_sha256'):
        raise ValueError('Recovered report bytes differ')
    return path


if __name__ == '__main__':
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument('action', choices=('prepare', 'run'))
    cli.add_argument('--run', required=True)
    cli.add_argument('--launch')
    cli.add_argument('--binary', default='/work/bin/aegisrt')
    cli.add_argument('--name', default=None)
    cli.add_argument('--prior-aggregate', type=int, default=584)
    cli.add_argument('--correction-from')
    args = cli.parse_args()
    result = (prepare(args.run,args.launch,name=args.name or DIRECTORY,prior_aggregate=args.prior_aggregate,
                      correction_from=args.correction_from) if args.action=='prepare'
              else run(args.run,args.binary,name=args.name))
    print(json.dumps(result, ensure_ascii=False))
