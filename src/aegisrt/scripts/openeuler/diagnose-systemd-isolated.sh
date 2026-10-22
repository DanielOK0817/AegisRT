#!/usr/bin/env bash
set -euo pipefail

# Read-only, bounded diagnosis of the systemd API-bus disconnect.
# This script never restarts, reloads, enables, or stops any service.
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
if (( $# != 0 || EUID != 0 )); then
  printf 'Usage: run this script as root without arguments.\n' >&2
  exit 2
fi
owner_uid=$(stat -c %u "$repo_root")
owner_gid=$(stat -c %g "$repo_root")
if [[ ! "$owner_uid" =~ ^[1-9][0-9]*$ || ! "$owner_gid" =~ ^[1-9][0-9]*$ ]]; then
  printf 'The source must have a non-root owner for evidence collection.\n' >&2
  exit 1
fi
work=$(mktemp -d /tmp/aegisrt-systemd-diag-XXXXXXXX)
return_evidence() {
  if [[ -f "$work/diagnostic.txt" && ! -L "$work/diagnostic.txt" ]]; then
    chown -- "$owner_uid:$owner_gid" "$work/diagnostic.txt"
  fi
  chown -- "$owner_uid:$owner_gid" "$work"
}
trap return_evidence EXIT
printf 'Evidence directory: %s\n' "$work"
probe() {
  printf '\nCOMMAND:'
  printf ' %q' "$@"
  printf '\n'
  local result=0
  timeout 6 "$@" || result=$?
  printf 'EXIT_CODE: %d\n' "$result"
}
{
  probe date -u
  probe cat /proc/loadavg /proc/pressure/cpu /proc/pressure/memory /proc/pressure/io
  probe free -m
  probe ps -p 1,830,933 -o pid,ppid,lstart,stat,wchan:24,comm
  probe cat /proc/1/stack
  probe ls -l /run/systemd/private /run/dbus/system_bus_socket
  probe systemctl show aegisrt.service sshd.service dbus.service --property=Id,ActiveState,SubState,MainPID,Result
  probe busctl --system --timeout=5 list --no-pager
  probe journalctl --since '15 minutes ago' --no-pager -n 80 _PID=1
  probe journalctl --since '15 minutes ago' --no-pager -n 80 -u dbus.service -u dbus-broker.service
  probe journalctl --since '15 minutes ago' --no-pager -n 30 -k --grep='oom|Out of memory|Killed process|BUG:|panic|hung task'
  probe journalctl --since '15 minutes ago' --no-pager -n 30 --grep='avc:.*denied.*(systemd|dbus)|SELinux.*(systemd|dbus)'
  # Preserve the known failure window even if user authentication takes time.
  probe journalctl --since '2026-09-22 21:03:00' --until '2026-09-22 21:07:00' --no-pager -n 40 --grep='avc:.*denied.*(systemd|dbus)|Got disconnect on API bus'
  probe journalctl --since '2026-09-22 21:03:00' --until '2026-09-22 21:07:00' --no-pager -n 40 -u dbus.service
  if command -v ausearch >/dev/null; then
    probe /bin/bash -c 'ausearch -m AVC -ts 21:03:00 -te 21:07:00 -c systemd --raw | awk '\''/type=AVC/ && /comm="systemd"/ && /(kernel-tests.txt|tclass=file|tclass=fd)/ { if (++shown <= 40) print }'\'''
    probe /bin/bash -c 'ausearch -m AVC -ts 21:19:00 -te 21:21:00 --raw | awk '\''/type=AVC/ && /(resourcecontrol|Qk6422Yc)/ { if (++shown <= 40) print }'\'''
  fi
} > "$work/diagnostic.txt" 2>&1
cat -- "$work/diagnostic.txt"
printf '\nRead-only diagnosis retained at %s\n' "$work"
