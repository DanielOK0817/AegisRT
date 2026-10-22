"""Isolated, no-model Linux process-tree smoke for runtime monitoring.

The controlled adapter reproduces the production adapter's chdir boundary.
It never starts AegisRT, opens a network connection or loads model credentials.
"""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace

from runtime_monitor import RuntimeMonitor


def main():
    if sys.platform != 'linux':
        raise SystemExit('Linux /proc is required')
    with tempfile.TemporaryDirectory(prefix='aegisrt-monitor-smoke-') as temporary:
        root = Path(temporary).resolve()
        integration, runs = root / 'integration', root / 'runs'
        integration.mkdir()
        runs.mkdir()
        workflow = runs / 'monitor-smoke'
        workflow.mkdir()
        task_id = 'monitor-smoke-query'
        task_dir = workflow / task_id
        task_dir.mkdir()
        control = workflow / '.control'
        control.mkdir()
        adapter = integration / 'adapter.py'
        adapter.write_text('''import argparse, os, subprocess, sys, time
from pathlib import Path
p=argparse.ArgumentParser()
p.add_argument('--role');p.add_argument('--run-dir');p.add_argument('--task-dir')
a=p.parse_args()
# Same lifecycle as the real live adapter: task work occurs after chdir.
os.chdir(a.task_dir)
child=subprocess.Popen([sys.executable,'-c',
    "import time; end=time.monotonic()+3; data=bytearray(4000000); exec('while time.monotonic()<end: pass')"])
Path('ready').write_text(str(os.getpid()))
try:
    child.wait(timeout=8)
    time.sleep(5)
finally:
    if child.poll() is None: child.terminate();child.wait()
''')
        daemon_script = root / 'controlled_daemon.py'
        daemon_script.write_text('''import subprocess, sys
args=sys.argv
adapter=args[args.index('--adapter')+1]
run_root=args[args.index('--run-root')+1]
task_dir=args[args.index('--task-dir')+1]
p=subprocess.Popen([sys.executable,adapter,'--role','query','--run-dir',run_root,'--task-dir',task_dir])
try: p.wait(timeout=10)
finally:
    if p.poll() is None: p.terminate();p.wait()
''')
        binary = Path(sys.executable).resolve()
        service = SimpleNamespace(binary=binary, integration_dir=integration, run_root=runs)
        task = {'id': task_id, 'role': 'query', 'status': 'RUNNING'}
        command = [str(binary), str(daemon_script), '-state-file', str(control / 'runtime.state'),
                   '-agents-file', str(control / 'agents.json'), '--adapter', str(adapter),
                   '--run-root', str(runs), '--task-dir', str(task_dir)]
        daemon = subprocess.Popen(command, cwd=workflow, start_new_session=True,
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            deadline = time.monotonic() + 5
            while not (task_dir / 'ready').is_file() and time.monotonic() < deadline:
                if daemon.poll() is not None:
                    raise AssertionError('controlled daemon exited before readiness')
                time.sleep(.05)
            monitor = RuntimeMonitor()
            rows = monitor._processes()
            bound_daemon = monitor._daemon(service, workflow, rows)
            if bound_daemon != daemon.pid:
                raise AssertionError('daemon executable/registry/state identity did not match')
            matched = monitor._task_processes(service, workflow, task, bound_daemon, rows)
            if len(matched) != 2:
                raise AssertionError('controlled adapter and its child were not both bound')
            first = monitor._measurement(matched, time.monotonic())
            time.sleep(.5)
            rows = monitor._processes()
            matched = monitor._task_processes(service, workflow, task, bound_daemon, rows)
            second = monitor._measurement(matched, time.monotonic())
            if not second['available'] or not second['memory_rss_bytes'] or not second['cpu_percent']:
                raise AssertionError('real process resource counters were not sampled')
            task['role'] = 'media'
            if monitor._task_processes(service, workflow, task, bound_daemon, rows):
                raise AssertionError('another agent role was incorrectly assigned this process')
            task.update(role='query', status='SUCCEEDED')
            if monitor._task_processes(service, workflow, task, bound_daemon, rows):
                raise AssertionError('terminal task was assigned current process usage')
            print(json.dumps({'success': True, 'source': '/proc real controlled process tree',
                              'first_sample': first, 'second_sample': second,
                              'role_mismatch_rejected': True, 'terminal_task_rejected': True,
                              'model_requests': 0, 'real_workflows_started': 0}))
        finally:
            try:
                os.killpg(daemon.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                daemon.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(daemon.pid, signal.SIGKILL)
                daemon.wait(timeout=3)


if __name__ == '__main__':
    main()
