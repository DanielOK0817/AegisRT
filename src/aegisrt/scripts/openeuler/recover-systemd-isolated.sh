#!/usr/bin/env bash
set -euo pipefail

# One D-Bus reconnection request, one new /bin/true unit, then isolated tests.
# Never restarts/reexecutes PID 1 or stops/restarts an existing service.
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
if (( $# != 0 || EUID != 0 )); then
  printf 'Usage: run this script as root without arguments.\n' >&2
  exit 2
fi
owner_uid=$(stat -c %u "$repo_root")
owner_gid=$(stat -c %g "$repo_root")
if [[ ! "$owner_uid" =~ ^[1-9][0-9]*$ || ! "$owner_gid" =~ ^[1-9][0-9]*$ ]]; then
  printf 'The isolated source directory must belong to an ordinary user.\n' >&2
  exit 1
fi
for name in diagnose-systemd-isolated.sh verify-core-isolated.sh; do
  test -f "$script_dir/$name" && test ! -L "$script_dir/$name"
done
for executable in timeout busctl systemd-run journalctl; do command -v "$executable" >/dev/null; done
work=$(mktemp -d /tmp/aegisrt-systemd-recovery-XXXXXXXX)
pid1_start=""
daemon_start=""
check_existing_processes() {
  [[ -n "$pid1_start" && -n "$daemon_start" &&
     $(cat /proc/1/comm) == systemd && $(awk '{print $22}' /proc/1/stat) == "$pid1_start" &&
     $(cat /proc/933/comm) == aegisrt && $(awk '{print $22}' /proc/933/stat) == "$daemon_start" ]]
}
finish() {
  local status=$?
  set +e
  local after_status=0 identity_status=0
  {
    date -u
    ps -p 1,933 -o pid,ppid,lstart,stat,comm
    for pid in 1 933; do
      printf 'PID %s comm and stat:\n' "$pid"
      cat "/proc/$pid/comm" "/proc/$pid/stat"
    done
    timeout 6 systemctl show aegisrt.service sshd.service --property=Id,ActiveState,SubState,MainPID,Result
    after_status=$?
    check_existing_processes
    identity_status=$?
    printf 'STATUS_QUERY_EXIT_CODE: %d\nIDENTITY_CHECK_EXIT_CODE: %d\n' "$after_status" "$identity_status"
  } > "$work/after.txt" 2>&1
  if (( status == 0 && (after_status != 0 || identity_status != 0) )); then status=1; fi
  printf '{"scope":"one-systemd-bus-reconnect-and-isolated-tests","exit_code":%d}\n' "$status" > "$work/result.json"
  for name in diagnostic.txt before.txt after.txt bus-before.txt bus-after.txt signal.txt probe-launch.txt probe-journal.txt tests.txt result.json; do
    if [[ -f "$work/$name" && ! -L "$work/$name" ]]; then chown -- "$owner_uid:$owner_gid" "$work/$name"; fi
  done
  chown -- "$owner_uid:$owner_gid" "$work"
  printf 'Recovery evidence retained at %s\n' "$work"
  trap - EXIT
  exit "$status"
}
trap finish EXIT
printf 'Recovery evidence directory: %s\n' "$work"
bash "$script_dir/diagnose-systemd-isolated.sh" > "$work/diagnostic.txt" 2>&1
cat -- "$work/diagnostic.txt"
if [[ $(cat /proc/1/comm) != systemd || $(cat /proc/933/comm) != aegisrt ]]; then
  printf 'Expected PID 1 or existing daemon identity changed; stop.\n' >&2
  exit 1
fi
pid1_start=$(awk '{print $22}' /proc/1/stat)
daemon_start=$(awk '{print $22}' /proc/933/stat)
ps -p 1,933 -o pid,ppid,lstart,stat,comm > "$work/before.txt"
timeout 6 busctl --system --timeout=5 list --no-pager > "$work/bus-before.txt"
manager_present() {
  awk '$1 == "org.freedesktop.systemd1" && $2 == "1" { found=1 } END { exit !found }' "$1"
}
if ! manager_present "$work/bus-before.txt"; then
  printf 'Requesting one PID 1 D-Bus reconnection using SIGUSR1.\n' | tee "$work/signal.txt"
  kill -USR1 1
else
  printf 'Manager already present; no signal sent.\n' > "$work/signal.txt"
fi
connected=false
for attempt in {1..10}; do
  if timeout 3 busctl --system --timeout=2 list --no-pager > "$work/bus-after.txt" &&
     manager_present "$work/bus-after.txt"; then
    connected=true
    break
  fi
  sleep 0.5
done
if [[ "$connected" != true ]]; then
  printf 'Systemd manager did not reconnect within the bounded observation period.\n' >&2
  exit 1
fi
check_existing_processes
unit="aegisrt-m4-probe-$(basename -- "$work")"
probe_status=0
timeout 20 systemd-run --quiet --wait --collect --unit="$unit" \
  --property=StandardOutput=journal --property=StandardError=journal \
  --property=RuntimeMaxSec=5s --property=TimeoutStopSec=5s \
  /bin/true > "$work/probe-launch.txt" 2>&1 || probe_status=$?
timeout 6 journalctl --unit="$unit.service" --no-pager --output=short-iso > "$work/probe-journal.txt" 2>&1
cat -- "$work/probe-launch.txt" "$work/probe-journal.txt"
if (( probe_status != 0 )); then exit "$probe_status"; fi
check_existing_processes
bash "$script_dir/verify-core-isolated.sh" 2>&1 | tee "$work/tests.txt"
check_existing_processes
