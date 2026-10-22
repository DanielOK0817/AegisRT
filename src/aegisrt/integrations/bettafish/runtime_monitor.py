"""Read-only runtime projections and Linux process measurements for the console.

Resource reservations remain separate from measurements. No process commands,
environment values, logs, page bodies, credentials or filesystem paths leave
this module. Stopped tasks have unknown current usage, never invented zeros.
"""
from __future__ import annotations

import base64
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import threading
import time

MAX_JSON_BYTES = 24_000_000
RESOURCE_KEYS = ('cpu_milli', 'memory_mib', 'llm_slots', 'token_budget')
HEX = re.compile(r'[0-9a-f]{64}\Z')
ACTIVE_TASKS = {'RUNNING', 'ADMITTED', 'CANCELING'}


def _integer(value):
    return value if type(value) is int and value >= 0 else None


def _resources(value):
    value = value if isinstance(value, dict) else {}
    return {key: _integer(value.get(key)) for key in RESOURCE_KEYS}


def _rows(value):
    return value if isinstance(value, list) else []


def _json(path, parent):
    try:
        resolved = path.resolve()
        if resolved.parent != parent.resolve() or not resolved.is_file():
            return None
        with resolved.open('rb') as stream:
            raw = stream.read(MAX_JSON_BYTES + 1)
        if len(raw) > MAX_JSON_BYTES:
            return None
        value = json.loads(raw)
        return value if isinstance(value, dict) else None
    except (OSError, ValueError, UnicodeError):
        return None


def _checkpoint(path, control):
    envelope = _json(path, control)
    if not envelope or (envelope.get('format'), envelope.get('version')) != ('aegisrt-state', 1):
        return None
    try:
        raw = base64.b64decode(envelope['payload'], validate=True)
        if len(raw) > 16_777_216 or hashlib.sha256(raw).hexdigest() != envelope.get('checksum'):
            return None
        value = json.loads(raw)
        return value if isinstance(value, dict) and value.get('format_version') == 2 else None
    except (KeyError, TypeError, ValueError, UnicodeError):
        return None


def _date(value):
    try:
        result = datetime.fromisoformat(value.replace('Z', '+00:00'))
        return result if result.tzinfo else None
    except (AttributeError, TypeError, ValueError):
        return None


def _elapsed(metadata, summary, now):
    elapsed = summary.get('elapsed_seconds')
    if type(elapsed) in (int, float) and 0 <= elapsed <= 31_536_000:
        return elapsed
    start = _date(metadata.get('started_at')) or _date(metadata.get('created_at'))
    end = _date(metadata.get('ended_at'))
    if start and (end or metadata.get('status') in ('QUEUED', 'RUNNING', 'CANCELING')):
        return round(max(0, ((end or now) - start).total_seconds()), 3)
    return None


def _empty_context():
    return {'available': False, 'page_count': None, 'logical_tokens': None,
            'pinned_tokens': None, 'revision': None, 'shared_page_count': None,
            'private_page_count': None, 'manifest_id': None, 'source': None}


def _context_heads(checkpoint, workflow_id, agents):
    """Only metadata belonging to this workflow and known agent heads is used."""
    if not checkpoint:
        return {}
    manifests = {item.get('id'): item for item in _rows(checkpoint.get('manifests')) if isinstance(item, dict)}
    store = checkpoint.get('store', {})
    if not isinstance(store, dict):
        return {}
    pages = {item.get('id'): item for item in _rows(store.get('pages')) if isinstance(item, dict)}
    tokens = {item.get('content_hash'): _integer(item.get('token_count'))
              for item in _rows(store.get('blobs')) if isinstance(item, dict)}
    result = {}
    for head in _rows(checkpoint.get('heads')):
        if not isinstance(head, dict) or head.get('workflow_id') != workflow_id or head.get('agent_id') not in agents:
            continue
        agent = head['agent_id']
        manifest = manifests.get(head.get('manifest_id'))
        if not manifest or (manifest.get('workflow_id'), manifest.get('agent_id')) != (workflow_id, agent):
            continue
        shared, private = manifest.get('shared_pages', []), manifest.get('private_pages', [])
        if not isinstance(shared, list) or not isinstance(private, list):
            continue
        logical, pinned, valid = 0, 0, True
        for kind, refs in (('shared', shared), ('private', private)):
            for ref in refs:
                page = pages.get(ref.get('page_id')) if isinstance(ref, dict) else None
                count = tokens.get(page.get('content_hash')) if page else None
                allowed = (page and ((kind == 'shared' and
                           ((page.get('scope') == 'system' and not page.get('workflow_id') and not page.get('agent_id')) or
                            (page.get('scope') == 'workflow' and page.get('workflow_id') == workflow_id and not page.get('agent_id')))) or
                           (kind == 'private' and page.get('scope') in ('agent', 'ephemeral') and
                            (page.get('workflow_id'), page.get('agent_id')) == (workflow_id, agent))))
                if not allowed or count is None:
                    valid = False
                    break
                logical += count
                pinned += count if ref.get('pin') is True else 0
        if valid and HEX.fullmatch(str(manifest.get('id', ''))) and _integer(manifest.get('revision')) is not None:
            result[agent] = {'available': True, 'page_count': len(shared) + len(private),
                             'logical_tokens': logical, 'pinned_tokens': pinned,
                             'revision': manifest['revision'], 'manifest_id': manifest['id'],
                             'shared_page_count': len(shared), 'private_page_count': len(private),
                             'source': 'context_checkpoint'}
    return result


def _events(folder, workflow_id, stages):
    document = _json(folder / 'events.json', folder) or {}
    result = {}
    pages = document.get('pages', [])
    if not isinstance(pages, list):
        return result
    for page in pages[:4096]:
        if not isinstance(page, dict) or not isinstance(page.get('events'), list):
            continue
        for event in page['events'][:256]:
            if not isinstance(event, dict) or event.get('workflow_id') != workflow_id:
                continue
            task_id = event.get('task_id')
            if task_id not in stages:
                continue
            attempt_id = str(event.get('attempt_id', ''))
            if not attempt_id.startswith(task_id + '-attempt-'):
                continue
            number = attempt_id.removeprefix(task_id + '-attempt-')
            if not number.isdecimal() or int(number) < 1 or len(number) > 5:
                continue
            payload = event.get('payload')
            if isinstance(payload, dict) and int(number) >= result.get(task_id, {}).get('attempt_count', 0):
                result[task_id] = {'attempt_count': int(number), 'payload': payload}
    return result


class RuntimeMonitor:
    def __init__(self, *, proc_root='/proc', monotonic=time.monotonic):
        self.proc_root = Path(proc_root)
        self.monotonic = monotonic
        self.lock = threading.Lock()
        self.host_previous = None
        self.process_previous = {}
        self.clock_ticks = os.sysconf('SC_CLK_TCK') if hasattr(os, 'sysconf') else 100
        self.page_bytes = os.sysconf('SC_PAGE_SIZE') if hasattr(os, 'sysconf') else 4096

    def _host(self):
        result = {'available': False, 'cpu_percent': None, 'memory_used_bytes': None,
                  'memory_total_bytes': None, 'source': 'procfs', 'cpu_basis': 'all_cores'}
        try:
            counters = [int(value) for value in (self.proc_root / 'stat').read_text().splitlines()[0].split()[1:9]]
            if len(counters) < 4:
                return result
            total, idle = sum(counters), counters[3] + (counters[4] if len(counters) > 4 else 0)
            previous = self.host_previous
            self.host_previous = (total, idle)
            if previous and total > previous[0] and idle >= previous[1]:
                result['cpu_percent'] = round(max(0, min(100, 100 * (1 - (idle - previous[1]) / (total - previous[0])))), 1)
            memory = {}
            for line in (self.proc_root / 'meminfo').read_text().splitlines():
                key, _, values = line.partition(':')
                pieces = values.split()
                if pieces and pieces[0].isdecimal():
                    memory[key] = int(pieces[0]) * 1024
            total_memory, available = memory.get('MemTotal'), memory.get('MemAvailable')
            if total_memory is not None and available is not None and 0 <= available <= total_memory:
                result.update(available=True, memory_total_bytes=total_memory,
                              memory_used_bytes=total_memory - available)
        except (OSError, ValueError, IndexError):
            pass
        return result

    def _processes(self):
        rows = {}
        try:
            folders = list(self.proc_root.iterdir())
        except OSError:
            return rows
        for folder in folders[:16384]:
            if not folder.name.isdecimal():
                continue
            try:
                raw = (folder / 'stat').read_text()
                fields = raw[raw.rindex(')') + 2:].split()
                rows[int(folder.name)] = {'ppid': int(fields[1]), 'ticks': int(fields[11]) + int(fields[12]),
                    'start': int(fields[19]), 'rss': max(0, int(fields[21])) * self.page_bytes,
                    'folder': folder}
            except (OSError, ValueError, IndexError):
                continue
        return rows

    def _daemon(self, service, folder, rows):
        control = folder / '.control'
        for pid, process in rows.items():
            try:
                arguments = (process['folder'] / 'cmdline').read_bytes().split(b'\0')
                if (not arguments or Path(os.fsdecode(arguments[0])).resolve() != service.binary or
                        (process['folder'] / 'exe').resolve() != service.binary):
                    continue
                state_index, registry_index = arguments.index(b'-state-file'), arguments.index(b'-agents-file')
                if (os.fsdecode(arguments[state_index + 1]) == str(control / 'runtime.state') and
                    os.fsdecode(arguments[registry_index + 1]) == str(control / 'agents.json')):
                    return pid
            except (OSError, ValueError, IndexError):
                continue
        return None

    def _task_processes(self, service, folder, task, daemon, rows):
        matched = []
        if daemon is None or task['status'] not in ACTIVE_TASKS:
            return matched
        directory = folder / task['id']
        if directory.resolve().parent != folder:
            return matched
        roots = set()
        integration = service.monitoring_integration(folder.name) if hasattr(service, 'monitoring_integration') else service.integration_dir
        adapter = str(integration / 'adapter.py').encode()
        for pid, process in rows.items():
            try:
                arguments = (process['folder'] / 'cmdline').read_bytes().split(b'\0')
                if adapter not in arguments or b'--role' not in arguments or b'--run-dir' not in arguments:
                    continue
                if (os.fsdecode(arguments[arguments.index(b'--role') + 1]) != task['role'] or
                    os.fsdecode(arguments[arguments.index(b'--run-dir') + 1]) != str(service.run_root) or
                    (process['folder'] / 'cwd').resolve() != directory.resolve()):
                    continue
                ancestor, seen = process['ppid'], set()
                while ancestor in rows and ancestor not in seen and ancestor != daemon:
                    seen.add(ancestor)
                    ancestor = rows[ancestor]['ppid']
                if ancestor == daemon:
                    roots.add(pid)
            except (OSError, ValueError, IndexError):
                continue
        for pid, process in rows.items():
            ancestor, seen = pid, set()
            while ancestor in rows and ancestor not in seen and ancestor not in roots:
                seen.add(ancestor)
                ancestor = rows[ancestor]['ppid']
            if ancestor in roots:
                matched.append((pid, process))
        return matched

    def _measurement(self, processes, now):
        result = {'available': bool(processes), 'cpu_percent': None, 'memory_rss_bytes': None,
                  'process_count': len(processes), 'cpu_basis': 'one_core', 'source': 'procfs'}
        if not processes:
            return result
        result['memory_rss_bytes'] = sum(item['rss'] for _, item in processes)
        cpu, complete = 0, True
        for pid, process in processes:
            key = (pid, process['start'])
            previous = self.process_previous.get(key)
            if previous and now > previous[0] and process['ticks'] >= previous[1]:
                cpu += 100 * (process['ticks'] - previous[1]) / self.clock_ticks / (now - previous[0])
            else:
                complete = False
            self.process_previous[key] = (now, process['ticks'])
        if complete:
            result['cpu_percent'] = round(cpu, 1)
        # PID start times protect against PID reuse; keep the poll cache bounded.
        if len(self.process_previous) > 4096:
            self.process_previous = {key: item for key, item in self.process_previous.items() if now - item[0] < 120}
        return result

    def workflow(self, service, workflow_id):
        folder = service.job_dir(workflow_id)
        status = service.status(workflow_id)
        monitor_folder = service.monitoring_folder(workflow_id) if hasattr(service, 'monitoring_folder') else folder
        metadata = _json(folder / 'entry.json', folder) or {}
        summary = _json(monitor_folder / 'summary.json', monitor_folder) or {}
        if summary.get('workflow_id') != workflow_id:
            summary = {}
        snapshot = _json(monitor_folder / 'task-snapshot.json', monitor_folder) or _json(folder / 'task-snapshot.json', folder) or {}
        specs = {}
        for row in _rows(snapshot.get('tasks')):
            spec = row.get('task') if isinstance(row, dict) else None
            if isinstance(spec, dict) and spec.get('workflow_id') == workflow_id:
                specs[spec.get('id')] = spec
        # A crashed node may leave an unreadable observation snapshot. The
        # original submitted graph still supplies dependency/resource declarations;
        # it never supplies a fabricated execution status or measurement.
        submitted = _json(monitor_folder / 'submitted-tasks.json', monitor_folder) or {}
        for spec in _rows(submitted.get('tasks')):
            if isinstance(spec, dict) and spec.get('workflow_id') == workflow_id:
                specs.setdefault(spec.get('id'), spec)
        control = monitor_folder / '.control'
        runtime_state = context_state = None
        if control.resolve().parent == monitor_folder:
            runtime_state = _checkpoint(control / 'runtime.state', control)
            context_state = _checkpoint(control / 'runtime.state.context', control)
        if runtime_state:
            graph = runtime_state.get('graph', {})
            graph_rows = _rows(graph.get('tasks')) if isinstance(graph, dict) else []
            if (not HEX.fullmatch(str(runtime_state.get('instance_id', ''))) or
                    any(not isinstance(row, dict) or not isinstance(row.get('spec'), dict) or
                        row['spec'].get('workflow_id') != workflow_id for row in graph_rows)):
                runtime_state = None
        if (not runtime_state or not context_state or
                runtime_state.get('instance_id') != context_state.get('runtime_instance_id')):
            context_state = None
        agents = {spec.get('agent_id') for spec in specs.values() if isinstance(spec.get('agent_id'), str)}
        heads = _context_heads(context_state, workflow_id, agents)
        stages = {row.get('task_id', workflow_id + '-' + row['role']) for row in status['tasks']}
        events = {**_events(folder, workflow_id, stages), **_events(monitor_folder, workflow_id, stages)}
        attempt_counters = {row.get('task_id'): _integer(row.get('number'))
                            for row in _rows((runtime_state or {}).get('attempts')) if isinstance(row, dict)}
        now = datetime.now(timezone.utc)
        with self.lock:
            host = self._host()
            processes = self._processes() if status['status'] in ('RUNNING', 'CANCELING') else {}
            daemon = self._daemon(service, monitor_folder, processes)
            tasks = []
            for row in status['tasks']:
                stage, role = row['role'], row['role'].split('-')[0]
                task_id = row.get('task_id', workflow_id + '-' + stage)
                spec = specs.get(task_id, {})
                agent = spec.get('agent_id')
                if agent not in agents or agent != 'bettafish-' + role:
                    agent = None
                event = events.get(task_id, {})
                payload = event.get('payload', {})
                task_context = heads.get(agent, _empty_context()).copy()
                dispatch = payload.get('context', {})
                if not task_context['available'] and isinstance(dispatch, dict):
                    refs, logical = _integer(dispatch.get('ref_count')), _integer(dispatch.get('logical_tokens'))
                    if refs is not None and logical is not None and HEX.fullmatch(str(dispatch.get('manifest_id', ''))):
                        task_context.update(available=True, page_count=refs, logical_tokens=logical,
                                            manifest_id=dispatch['manifest_id'], source='attempt_dispatch')
                directory = (folder / task_id).resolve()
                adapter_state = _json(directory / 'state.json', directory) if directory.parent == folder else None
                if not adapter_state or tuple(adapter_state.get(key) for key in ('workflow_id', 'task_id', 'role', 'mode')) != (workflow_id, task_id, role, 'live'):
                    adapter_state = {}
                attempts = attempt_counters.get(task_id)
                if attempts is None:
                    attempts = event.get('attempt_count', 1 if adapter_state else 0)
                timing = payload.get('timing', {})
                timing = timing if isinstance(timing, dict) else {}
                output = payload.get('output', {})
                output = output if isinstance(output, dict) else {}
                task = {'id': task_id, 'stage': stage, 'role': role, 'agent_id': agent,
                        'status': row['status'], 'dependencies': [identifier.removeprefix(workflow_id + '-')
                          for identifier in _rows(spec.get('dependencies')) if isinstance(identifier, str) and identifier in stages],
                        'resources': _resources(spec.get('resources')), 'attempt_count': attempts,
                        'max_attempts': _integer(spec.get('max_attempts')), 'context': task_context,
                        'output': {key: _integer(output.get(key)) for key in ('artifact_count', 'content_bytes')},
                        'timing': {'started_at': (_date(timing.get('started_at')) or _date(adapter_state.get('started_at'))),
                                   'finished_at': (_date(timing.get('finished_at')) or _date(adapter_state.get('completed_at'))),
                                   'queue_ms': _integer(timing.get('queue_ms')), 'run_ms': _integer(timing.get('run_ms'))}}
                task['timing'] = {key: value.isoformat() if isinstance(value, datetime) else value for key, value in task['timing'].items()}
                task['measurement'] = self._measurement(self._task_processes(service, folder, task, daemon, processes), self.monotonic())
                tasks.append(task)
        saved_runtime = summary.get('runtime', {})
        saved_runtime = saved_runtime if isinstance(saved_runtime, dict) else {}
        scheduler = (runtime_state or {}).get('scheduler', {})
        scheduler = scheduler if isinstance(scheduler, dict) else {}
        stats = saved_runtime.get('stats', {})
        stats = stats if isinstance(stats, dict) else {}
        # Context totals describe current agent heads, or distinct captured dispatch
        # manifests when a private checkpoint was deliberately not exported.
        unique = {item['context'].get('manifest_id'): item['context'] for item in tasks if item['context']['available']}
        return {'success': True, 'schema': 'aegisrt.presentation-monitor/v1', 'workflow_id': workflow_id,
                'sampled_at': now.isoformat(), 'elapsed_seconds': _elapsed(metadata, summary, now), 'host': host,
                'runtime': {'live': daemon is not None, 'policy': scheduler.get('policy_name', stats.get('policy')),
                            'capacity': _resources(scheduler.get('capacity', stats.get('capacity'))),
                            'allocated': _resources(scheduler.get('allocated', stats.get('allocated')))},
                'context': {'available': bool(unique), 'logical_tokens': sum(item['logical_tokens'] for item in unique.values()) if unique else None,
                            'page_count': sum(item['page_count'] for item in unique.values()) if unique else None,
                            'counter_identity': context_state.get('counter_identity') if context_state else None,
                            'source': 'context_checkpoint' if heads else ('attempt_dispatch' if unique else None)},
                'tasks': tasks}
