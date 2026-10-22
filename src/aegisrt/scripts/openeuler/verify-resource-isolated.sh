#!/usr/bin/env bash
set -euo pipefail

# Uses a new transient unit only. No existing service is stopped or changed.
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
if (( $# != 0 )); then
  printf 'Usage: sudo %s\n' "$0" >&2
  exit 2
fi
if (( EUID != 0 )); then
  printf 'Run explicitly as root to create a new delegated transient unit.\n' >&2
  exit 2
fi
if [[ ! -r /etc/os-release ]]; then
  printf 'No operating-system identity available.\n' >&2
  exit 1
fi
os_id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
if [[ "$os_id" != "openEuler" && "$os_id" != "openeuler" ]]; then
  printf 'This verification requires openEuler.\n' >&2
  exit 1
fi
if [[ $(stat -fc %T /sys/fs/cgroup) != cgroup2fs ]]; then
  printf 'Unified cgroup v2 is required.\n' >&2
  exit 1
fi
go_bin=""
for candidate in /usr/local/bin/go /usr/local/go/bin/go /usr/bin/go; do
  if [[ -x "$candidate" ]]; then
    go_bin="$candidate"
    break
  fi
done
if [[ -z "$go_bin" ]]; then
  printf 'No Go executable found in a fixed installation directory.\n' >&2
  exit 1
fi
for executable in systemd-run setsid restorecon matchpathcon; do
  command -v "$executable" >/dev/null
done
if [[ -L /usr/local/bin || $(stat -c %u /usr/local/bin) != 0 ]] ||
   [[ ",$(findmnt -T /usr/local/bin -no OPTIONS)," == *,noexec,* ]]; then
  printf 'The isolated executable parent is unsuitable.\n' >&2
  exit 1
fi
work=$(mktemp -d /tmp/aegisrt-m4-XXXXXXXX)
exec_dir=""
return_evidence() {
  local status=$? cleanup_status=0
  set +e
  if [[ "$exec_dir" =~ ^/usr/local/bin/aegisrt-m4-[A-Za-z0-9]{8}$ && ! -L "$exec_dir" ]]; then
    rm -f -- "$exec_dir/resourcecontrol.test" || cleanup_status=1
    rmdir -- "$exec_dir" || cleanup_status=1
  fi
  printf 'EXECUTABLE_DIRECTORY_CLEANUP_EXIT_CODE: %d\n' "$cleanup_status" > "$work/cleanup.txt"
  if (( status == 0 && cleanup_status != 0 )); then status=1; fi
  printf '{"scope":"isolated-cgroup-cleanup","exit_code":%d,"cleanup_exit_code":%d,"daemon_crash_tested":false}\n' "$status" "$cleanup_status" > "$work/result.json"
  if [[ "${SUDO_UID:-}" =~ ^[1-9][0-9]*$ && "${SUDO_GID:-}" =~ ^[1-9][0-9]*$ ]]; then
    for name in os-release.txt kernel.txt go-version.txt component-tests.jsonl execution-location.txt launch.txt kernel-tests.txt log-validation.txt cleanup.txt result.json; do
      if [[ -f "$work/$name" && ! -L "$work/$name" ]]; then
        chown -- "$SUDO_UID:$SUDO_GID" "$work/$name"
      fi
    done
    chown -- "$SUDO_UID:$SUDO_GID" "$work"
  fi
  trap - EXIT
  exit "$status"
}
trap return_evidence EXIT
unit="aegisrt-m4-$(basename "$work")"
printf 'Evidence directory: %s\n' "$work"
cp -- /etc/os-release "$work/os-release.txt"
uname -sr > "$work/kernel.txt"
cd -- "$repo_root"
"$go_bin" version > "$work/go-version.txt"
export GOMAXPROCS=2 GOMEMLIMIT=768MiB
"$go_bin" test -p 1 ./internal/executor ./internal/resourcecontrol ./internal/worker ./internal/scheduler ./internal/runtimecore ./internal/controlplane -count=1 -json > "$work/component-tests.jsonl"
"$go_bin" test -p 1 -c -o "$work/resourcecontrol.test" ./internal/resourcecontrol
exec_dir=$(mktemp -d /usr/local/bin/aegisrt-m4-XXXXXXXX)
chmod 0755 "$exec_dir"
install -m 0755 -- "$work/resourcecontrol.test" "$exec_dir/resourcecontrol.test"
restorecon -- "$exec_dir" "$exec_dir/resourcecontrol.test"
matchpathcon -V "$exec_dir" "$exec_dir/resourcecontrol.test"
{
  ls -ldZ -- "$exec_dir" "$exec_dir/resourcecontrol.test"
  sha256sum "$work/resourcecontrol.test" "$exec_dir/resourcecontrol.test"
} > "$work/execution-location.txt"
status=0
timeout 160 systemd-run --quiet --wait --collect --unit="$unit" \
  --property=StandardOutput=journal --property=StandardError=journal \
  --property=Delegate=yes --property=TasksMax=256 --property=MemoryMax=512M \
  --property=RuntimeMaxSec=2min --property=TimeoutStopSec=10s \
  --setenv=AEGISRT_KERNEL_M4=1 \
  "$exec_dir/resourcecontrol.test" -test.v -test.timeout=90s -test.run='^TestKernelAttemptCleanup$' \
  > "$work/launch.txt" 2>&1 || status=$?
journal_status=0
timeout 10 journalctl --unit="$unit.service" --no-pager --output=short-iso > "$work/kernel-tests.txt" 2>&1 || journal_status=$?
if (( status == 0 && journal_status != 0 )); then status=$journal_status; fi
validation_status=0
for test_name in TestKernelAttemptCleanup TestKernelAttemptCleanup/success TestKernelAttemptCleanup/exit-failure TestKernelAttemptCleanup/cancel TestKernelAttemptCleanup/timeout TestKernelAttemptCleanup/start-hook-failure TestKernelAttemptCleanup/start-failure; do
  if grep -Fq -- "--- PASS: $test_name (" "$work/kernel-tests.txt"; then
    printf 'PASS marker present: %s\n' "$test_name"
  else
    printf 'PASS marker missing: %s\n' "$test_name"
    validation_status=1
  fi
done > "$work/log-validation.txt"
if (( status == 0 && validation_status != 0 )); then status=1; fi
cat -- "$work/launch.txt"
cat -- "$work/kernel-tests.txt"
cat -- "$work/log-validation.txt"
printf 'Evidence retained at %s\n' "$work"
exit "$status"
