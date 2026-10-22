#!/usr/bin/env bash
set -euo pipefail

# A separate build and a new transient service; never controls aegisrt.service.
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
go_bin=""
for candidate in /usr/local/bin/go /usr/local/go/bin/go /usr/bin/go; do
  if [[ -x "$candidate" ]]; then go_bin="$candidate"; break; fi
done

if [[ $# == 1 && "$1" == --build ]]; then
  if (( EUID == 0 )) || [[ -z "$go_bin" ]]; then
    printf 'Build as the ordinary source owner with Go installed.\n' >&2
    exit 2
  fi
  build_dir=$(mktemp -d "$repo_root/.m4-f4-build-XXXXXXXX")
  cd -- "$repo_root"
  export GOMAXPROCS=2 GOMEMLIMIT=768MiB
  "$go_bin" build -p 1 -trimpath -o "$build_dir/aegisrt" ./cmd/aegisrt
  "$go_bin" build -p 1 -trimpath -o "$build_dir/aegisbench" ./cmd/aegisbench
  "$go_bin" version -m "$build_dir/aegisrt" > "$build_dir/daemon-build.txt"
  "$go_bin" version -m "$build_dir/aegisbench" > "$build_dir/harness-build.txt"
  git rev-parse HEAD > "$build_dir/base-commit.txt"
  find cmd internal -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > "$build_dir/source-files.sha256"
  sha256sum go.mod go.sum >> "$build_dir/source-files.sha256"
  (cd -- "$build_dir" && sha256sum aegisrt aegisbench > SHA256SUMS)
  pointer=$(mktemp "$repo_root/.m4-f4-pointer-XXXXXXXX")
  basename -- "$build_dir" > "$pointer"
  mv -T -- "$pointer" "$repo_root/.m4-f4-current"
  printf 'Prepared isolated F4 binaries: %s\n' "$build_dir"
  exit 0
fi
if (( $# != 0 || EUID != 0 )); then
  printf 'Usage: bash %s --build; then sudo bash %s\n' "$0" "$0" >&2
  exit 2
fi
if [[ ! -r /etc/os-release ]] || ! grep -Eq '^ID="?(openEuler|openeuler)"?$' /etc/os-release; then
  printf 'This verification requires openEuler.\n' >&2
  exit 1
fi
if [[ $(stat -fc %T /sys/fs/cgroup) != cgroup2fs ]]; then
  printf 'Unified cgroup v2 is required.\n' >&2
  exit 1
fi
command -v systemd-run >/dev/null
command -v restorecon >/dev/null
command -v matchpathcon >/dev/null
if [[ -L /usr/local/bin || $(stat -c %u /usr/local/bin) != 0 ]] ||
   [[ ",$(findmnt -T /usr/local/bin -no OPTIONS)," == *,noexec,* ]]; then
  printf 'The isolated executable parent is unsuitable.\n' >&2
  exit 1
fi
pointer="$repo_root/.m4-f4-current"
if [[ ! -f "$pointer" || -L "$pointer" ]]; then
  printf 'Run the ordinary-user --build step first.\n' >&2
  exit 1
fi
read -r build_name < "$pointer"
if [[ ! "$build_name" =~ ^\.m4-f4-build-[A-Za-z0-9]{8}$ ]]; then exit 1; fi
build_dir="$repo_root/$build_name"
if [[ ! -d "$build_dir" || -L "$build_dir" ]]; then exit 1; fi
run_uid=$(stat -c %u "$repo_root")
run_gid=$(stat -c %g "$repo_root")
if [[ ! "$run_uid" =~ ^[1-9][0-9]*$ || ! "$run_gid" =~ ^[1-9][0-9]*$ ]]; then
  printf 'The isolated source directory must belong to an ordinary user.\n' >&2
  exit 1
fi
for binary in aegisrt aegisbench; do
  if [[ ! -f "$build_dir/$binary" || -L "$build_dir/$binary" || ! -x "$build_dir/$binary" ]]; then exit 1; fi
done
work=$(mktemp -d /tmp/aegisrt-f4-XXXXXXXX)
chmod 0755 "$work"
mkdir -m 0700 "$work/output"
chown -- "$run_uid:$run_gid" "$work/output"
exec_dir=""
execution_status="null"
return_evidence() {
  local status=$? cleanup_status=0
  set +e
  if [[ "$exec_dir" =~ ^/usr/local/bin/aegisrt-f4-[A-Za-z0-9]{8}$ && ! -L "$exec_dir" ]]; then
    rm -f -- "$exec_dir/aegisrt" "$exec_dir/aegisbench" || cleanup_status=1
    rmdir -- "$exec_dir" || cleanup_status=1
  fi
  printf 'EXECUTABLE_DIRECTORY_CLEANUP_EXIT_CODE: %d\n' "$cleanup_status" > "$work/cleanup.txt"
  if (( status == 0 && cleanup_status != 0 )); then status=1; fi
  printf '{"scope":"isolated-f4-smoke","execution_exit_code":%s,"final_exit_code":%d,"cleanup_exit_code":%d,"trial_results_require_inspection":true}\n' "$execution_status" "$status" "$cleanup_status" > "$work/result.json"
  for name in os-release.txt kernel.txt daemon-build.txt harness-build.txt base-commit.txt source-files.sha256 SHA256SUMS execution-location.txt launch.txt supervisor.txt cleanup.txt result.json; do
    if [[ -f "$work/$name" && ! -L "$work/$name" ]]; then chown -- "$run_uid:$run_gid" "$work/$name"; fi
  done
  chown -- "$run_uid:$run_gid" "$work"
  trap - EXIT
  exit "$status"
}
trap return_evidence EXIT
exec_dir=$(mktemp -d /usr/local/bin/aegisrt-f4-XXXXXXXX)
chmod 0755 "$exec_dir"
for binary in aegisrt aegisbench; do
  install -m 0755 -- "$build_dir/$binary" "$exec_dir/$binary"
done
restorecon -- "$exec_dir" "$exec_dir/aegisrt" "$exec_dir/aegisbench"
matchpathcon -V "$exec_dir" "$exec_dir/aegisrt" "$exec_dir/aegisbench"
ls -ldZ -- "$exec_dir" "$exec_dir/aegisrt" "$exec_dir/aegisbench" > "$work/execution-location.txt"
for name in daemon-build.txt harness-build.txt base-commit.txt source-files.sha256; do
  install -m 0644 -- "$build_dir/$name" "$work/$name"
done
cp -- /etc/os-release "$work/os-release.txt"
uname -sr > "$work/kernel.txt"
(cd -- "$exec_dir" && sha256sum aegisrt aegisbench > "$work/SHA256SUMS")
if ! cmp -s -- "$build_dir/SHA256SUMS" "$work/SHA256SUMS"; then
  printf 'Copied executable hashes differ from the prepared build.\n' >&2
  exit 1
fi
unit=$(basename -- "$work")
printf 'Isolated F4 evidence directory: %s\n' "$work"
status=0
timeout 540 systemd-run --quiet --wait --collect --unit="$unit" \
  --property=StandardOutput=journal --property=StandardError=journal \
  --property="User=$run_uid" --property="Group=$run_gid" \
  --property='Delegate=cpu memory pids' --property="RuntimeDirectory=$unit" \
  --property=RuntimeDirectoryMode=0700 --property=PrivateNetwork=yes \
  --property=NoNewPrivileges=yes --property=KillMode=control-group \
  --property=TasksMax=256 --property=MemoryMax=768M --property=RuntimeMaxSec=8min \
  --property=TimeoutStopSec=30s --setenv=GOMAXPROCS=2 --setenv=GOMEMLIMIT=512MiB \
  "$exec_dir/aegisbench" fault-kernel --profile smoke --output "$work/output" \
  --seeds 104729,104743 --repetitions 1 > "$work/launch.txt" 2>&1 || status=$?
execution_status=$status
journal_status=0
timeout 10 journalctl --unit="$unit.service" --no-pager --output=short-iso > "$work/supervisor.txt" 2>&1 || journal_status=$?
if (( status == 0 && journal_status != 0 )); then status=$journal_status; fi
cat -- "$work/launch.txt"
cat -- "$work/supervisor.txt"
printf 'Inspect all paired trial outcomes; evidence retained at %s\n' "$work"
exit "$status"
