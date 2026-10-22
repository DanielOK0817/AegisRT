"""Read registered real workflows in the main runtime console.

This observer does not instantiate WorkflowService, write entry metadata, launch
tasks, or support any mutation endpoint. The report requires both independent
round verification and the saved automatic browser-QA receipt bound to bytes.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import sys
import threading

STAGES = ('query', 'media', 'insight', 'forum', 'query-feedback',
          'media-feedback', 'insight-feedback', 'forum-final', 'report')
NAMES = dict(zip(STAGES, ('信息检索', '媒体分析', '评论分析', '汇总讨论',
                         '信息检索补充研究', '媒体分析补充研究', '评论分析补充研究', '最终讨论', '生成报告')))
MAX_BYTES = 8_000_000


def read_json(path):
    with Path(path).open('rb') as stream:
        raw = stream.read(MAX_BYTES + 1)
    if len(raw) > MAX_BYTES:
        raise ValueError('Observer input exceeds size limit')
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError('Observer expected an object')
    return value


def sha(path):
    with Path(path).open('rb') as stream:
        value = hashlib.sha256()
        for block in iter(lambda: stream.read(65536), b''):
            value.update(block)
    return value.hexdigest()


class RunReader:
    def __init__(self, workflow_id, run_dir, launch_file, qa_receipt, env_file, stage_manifest_file=None):
        if not re.fullmatch(r'bf-auto-(small|default)-[a-f0-9]{18}', workflow_id):
            raise ValueError('Unsupported actual workflow identity')
        self.workflow_id = workflow_id
        self.run_dir = Path(run_dir).resolve(strict=True)
        if self.run_dir.name != workflow_id:
            raise ValueError('Run directory identity differs')
        self.launch_file = Path(launch_file).resolve(strict=True)
        self.qa_receipt = Path(qa_receipt).resolve()
        self.env_file = Path(env_file).resolve()
        self.stage_manifest_file = Path(stage_manifest_file).resolve() if stage_manifest_file else self.launch_file.parents[2] / 'stage-manifest.json'
        self.lock = threading.RLock()
        launch = read_json(self.launch_file)
        if launch.get('workflow_id') != workflow_id or launch.get('task_count') != 9:
            raise ValueError('Actual launch does not identify this nine-task run')

    def job_dir(self, workflow_id):
        if workflow_id != self.workflow_id:
            raise ValueError('Unknown observed workflow')
        return self.run_dir

    def report_path(self, workflow_id):
        root = self.job_dir(workflow_id)
        from report_recovery import view, report_path
        if view(root):
            return report_path(root)
        launch = read_json(self.launch_file)
        verification = read_json(root / 'verification.json')
        summary = read_json(root / 'summary.json')
        receipt = read_json(self.qa_receipt)
        report_dir = (root / (workflow_id + '-report')).resolve(strict=True)
        if report_dir.parent != root:
            raise ValueError('Report directory escapes run')
        report = report_dir / 'report.html'
        manifest = read_json(report_dir / 'result.json')
        if verification.get('workflow_id') != workflow_id or verification.get('status') != 'passed':
            raise ValueError('Independent round verification has not passed')
        if summary.get('workflow_id') != workflow_id or summary.get('status') != 'completed':
            raise ValueError('Actual coordinator has not completed')
        if receipt.get('schema') != 'bettafish.automatic-report-browser-qa/v1' or receipt.get('status') != 'passed':
            raise ValueError('Independent browser receipt has not passed')
        if receipt.get('workflow_id') != workflow_id or receipt.get('code_revision') != launch.get('code_revision'):
            raise ValueError('Browser receipt belongs to another run')
        if not all(receipt.get(key) is True for key in ('run_identity_verified', 'frozen_input_hashes_verified',
                                                       'code_revision_record_hash_verified', 'automatic_review_identity_verified')):
            raise ValueError('Browser receipt identity/review binding incomplete')
        if receipt.get('runtime_tasks_verified') != 9 or receipt.get('model_requests') != 0 or receipt.get('workflow_submissions') != 0:
            raise ValueError('Unexpected browser verification scope')
        if receipt.get('report_content_edited') is not False or receipt.get('evidence_reconstructed') is not False:
            raise ValueError('Receipt does not verify original report bytes')
        if receipt.get('automatic_evidence_review', {}).get('status') != 'passed_saved_model_response_verified':
            raise ValueError('Saved model review has not been verified')
        if not receipt.get('cases') or any(row.get('status') != 'passed' for row in receipt['cases']):
            raise ValueError('Browser viewport checks incomplete')
        for key in ('console_errors', 'page_errors', 'blocked_requests'):
            if receipt.get(key) != []:
                raise ValueError('Browser receipt contains errors')
        if (manifest.get('schema'), manifest.get('workflow_id'), manifest.get('task_id'), manifest.get('role'), manifest.get('mode')) != (
                'bettafish.task-result/v1', workflow_id, workflow_id + '-report', 'report', 'live'):
            raise ValueError('Report identity differs')
        file_record = manifest.get('files', {}).get('report', {})
        if Path(file_record.get('path', '')).resolve() != report:
            raise ValueError('Report manifest points outside this run')
        paths = {'launch': self.launch_file, 'stage-manifest': self.stage_manifest_file, 'verification': root / 'verification.json',
                 'summary': root / 'summary.json', 'task-snapshot': root / 'task-snapshot.json',
                 'input-manifest': root / 'inputs/manifest.json', 'report': report,
                 'content': report_dir / 'report-content.json', 'audit': report_dir / 'report-audit.json',
                 'bundle': report_dir / 'report-evidence.json', 'response': report_dir / 'model-report-response.txt',
                 'result': report_dir / 'result.json', 'report-state': report_dir / 'state.json',
                 'report-budget': report_dir / 'model-budget.json', 'review': report_dir / 'report-evidence-review.json'}
        for key in ('review-input', 'review-response'):
            name = str(receipt.get('input_paths', {}).get(key, '')).replace('\\', '/').rsplit('/', 1)[-1]
            if not re.fullmatch(r'(?:report-review-input|model-report-review-response)-[1-5]\.(?:json|txt)', name):
                raise ValueError('Unknown saved review filename')
            paths[key] = report_dir / name
        for key, source in paths.items():
            if sha(source) != receipt.get('input_sha256', {}).get(key):
                raise ValueError('Saved browser receipt no longer binds current bytes')
        inputs = read_json(root / 'inputs/manifest.json')
        for key in ('comments', 'provenance'):
            row = inputs.get('files', {}).get(key, {})
            if row.get('path') != key + '.json' or sha(root / 'inputs' / row['path']) != row.get('sha256'):
                raise ValueError('Frozen original input bytes changed')
            if row['sha256'] != receipt.get('input_files_sha256', {}).get(key):
                raise ValueError('Browser receipt does not bind original input bytes')
        digest = sha(report)
        if digest != file_record.get('sha256') or digest != verification.get('report', {}).get('sha256'):
            raise ValueError('Report hash differs across actual evidence')
        return report

    def status(self, workflow_id):
        root = self.job_dir(workflow_id)
        launch = read_json(self.launch_file)
        snapshot = read_json(root / 'task-snapshot.json') if (root / 'task-snapshot.json').is_file() else {'tasks': []}
        statuses = {stage: 'WAITING' for stage in STAGES}
        for row in snapshot.get('tasks', []):
            task = row.get('task', {})
            identifier = task.get('id', '')
            stage = identifier.removeprefix(workflow_id + '-')
            if stage in statuses and identifier == workflow_id + '-' + stage and task.get('workflow_id') == workflow_id:
                statuses[stage] = row.get('status', 'WAITING')
        attempts = 0
        for stage in STAGES:
            budget_path = root / (workflow_id + '-' + stage) / 'model-budget.json'
            if budget_path.is_file():
                attempts += read_json(budget_path).get('attempts', 0)
        summary = read_json(root / 'summary.json') if (root / 'summary.json').is_file() else {}
        if summary.get('workflow_id') == workflow_id:
            attempts = max(attempts, summary.get('model_usage', {}).get('provider_attempts', 0))
        from report_recovery import view
        recovery = view(root)
        if recovery:
            statuses['report'] = recovery['status']
            attempts += recovery['model_requests']
        report_url = None
        state, message = 'RUNNING', '本轮真实验收只读观察，报告在独立审核通过后开放。'
        if (not recovery and summary.get('workflow_id') == workflow_id and summary.get('status') == 'error') or any(s in ('FAILED', 'CANCELED') for s in statuses.values()):
            state, message = 'FAILED', '本轮真实执行未完成，报告保持关闭，已保存研究内容保留。'
        elif all(s == 'SUCCEEDED' for s in statuses.values()):
            try:
                self.report_path(workflow_id)
                state, message = 'SUCCEEDED', '九项任务、独立验收与报告浏览器核验均已通过。'
                report_url = '/api/aegisrt/workflows/' + workflow_id + '/report'
            except (OSError, KeyError, ValueError, TypeError):
                message = '九项任务已结束，正在等待独立验收；报告保持关闭。'
        if recovery:
            message = recovery['message'] + ('恢复及独立审核已通过。' if state == 'SUCCEEDED' else
                       '恢复失败，已停止。' if state == 'FAILED' else '正在恢复或核验报告。')
        return {'success': True, 'workflow_id': workflow_id, 'query': launch['topic'], 'status': state,
                'tasks': [{'role': stage, 'name': NAMES[stage], 'status': statuses[stage],
                           'task_id': recovery['task_id'] if stage == 'report' and recovery else workflow_id + '-' + stage}
                          for stage in STAGES],
                'model_requests': {'attempts': attempts, 'limit': launch['max_model_requests']},
                'sample_count': launch['sample_count'], 'report_url': report_url, 'message': message, 'recovery': recovery}

    def monitoring_folder(self, workflow_id):
        from report_recovery import view, folder_for
        root = self.job_dir(workflow_id)
        return folder_for(root) if view(root) else root

    def monitoring_integration(self, workflow_id):
        from report_recovery import view, record, check_code
        root = self.job_dir(workflow_id)
        if view(root):
            auth = record(root)
            check_code(auth)
            return Path(auth['integration_dir'])
        return self.integration_dir


def load_registry(path, *, env_file, binary):
    """Read an operator-owned registry; never copy or rewrite actual run files."""
    if not path.is_file():
        return {}, None
    registry = read_json(path)
    if registry.get('schema') != 'aegisrt.observed-workflows/v1':
        raise ValueError('Unsupported observed workflow registry')
    readers = {}
    for row in registry['workflows']:
        workflow = row['workflow_id']
        stage = Path(row['stage_dir']).resolve(strict=True)
        if workflow in readers:
            raise ValueError('Duplicate registered workflow')
        launch_path = stage / 'private/rounds' / (workflow + '.json')
        run_path = stage / 'private/runs' / workflow
        reader = RunReader(workflow, run_path, launch_path, row['qa_receipt'], env_file, stage / 'stage-manifest.json')
        if reader.run_dir != run_path.absolute() or reader.launch_file != launch_path.absolute():
            raise ValueError('Registered workflow path is redirected')
        launch = read_json(reader.launch_file)
        revision = launch.get('code_revision', '')
        if not re.fullmatch(r'code-[a-f0-9]{16}', revision):
            raise ValueError('Invalid registered code revision')
        manifest = read_json(reader.stage_manifest_file)
        records = manifest.get('revisions', {}).get(revision, [])
        adapter_record = next((r for r in records if r.get('path') == 'integration/adapter.py'), None)
        frozen = (stage / 'revisions' / revision / 'integration').resolve(strict=True)
        if (not frozen.is_relative_to(stage) or not adapter_record or
                sha(frozen / 'adapter.py') != adapter_record['sha256']):
            raise ValueError('Registered frozen adapter identity differs')
        reader.integration_dir = frozen
        reader.run_root = reader.run_dir.parent
        reader.binary = binary
        readers[workflow] = reader
    default = registry.get('default_workflow_id')
    if default is not None and default not in readers:
        raise ValueError('Default workflow is not registered')
    return readers, default


