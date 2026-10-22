#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
output_rel=""

while (( $# > 0 )); do
  case "$1" in
    --output-dir)
      if (( $# < 2 )); then
        printf 'verify-platform: --output-dir requires a value\n' >&2
        exit 2
      fi
      output_rel="$2"
      shift 2
      ;;
    -h|--help)
      printf 'Usage: sudo %s [--output-dir experiments/raw/openeuler/NAME]\n' "$0"
      exit 0
      ;;
    *)
      printf 'verify-platform: unsupported argument: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if (( EUID != 0 )); then
  printf 'verify-platform: run explicitly as root to read the service credential\n' >&2
  exit 1
fi

nonce=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
if [[ ! "$nonce" =~ ^[0-9a-f]{8}$ ]]; then
  printf 'verify-platform: secure run ID generation failed\n' >&2
  exit 1
fi
if [[ -z "$output_rel" ]]; then
  run_id="openeuler-$(date -u +%Y%m%dT%H%M%SZ)-$nonce"
  output_rel="experiments/raw/openeuler/$run_id"
fi

if [[ ! "$output_rel" =~ ^experiments/raw/openeuler/[A-Za-z0-9._-]+$ || "$output_rel" == *..* ]]; then
  printf 'verify-platform: output must be one run directory below experiments/raw/openeuler\n' >&2
  exit 2
fi
output_dir="$repo_root/$output_rel"
if [[ -e "$output_dir" || -L "$repo_root/experiments" || -L "$repo_root/experiments/raw" ]]; then
  printf 'verify-platform: refusing existing output or symlinked output parent\n' >&2
  exit 1
fi
output_uid=0
output_gid=0
if [[ -n "${SUDO_UID:-}" || -n "${SUDO_GID:-}" ]]; then
  if [[ ! "${SUDO_UID:-}" =~ ^[0-9]+$ || ! "${SUDO_GID:-}" =~ ^[0-9]+$ ]]; then
    printf 'verify-platform: invalid sudo caller identity\n' >&2
    exit 1
  fi
  output_uid=$SUDO_UID
  output_gid=$SUDO_GID
fi
mkdir -p -- "$output_dir"
chmod 0700 "$output_dir"

auth_config="$output_dir/.curl-auth"
graph_file="$output_dir/.task-graph.json"
tasks_file="$output_dir/.tasks.json"
submission_file="$output_dir/.submission.json"
release_output() {
  if [[ -d "$output_dir" && ! -L "$output_dir" ]]; then
    chown -R -- "$output_uid:$output_gid" "$output_dir"
  fi
}
cleanup() {
  rm -f -- "$auth_config" "$graph_file" "$tasks_file" "$submission_file"
  release_output || true
}
trap cleanup EXIT

write_summary() {
  local overall="$1" preflight="$2" service="$3" health="$4" lab="$5" reason="$6"
  if [[ ! "$reason" =~ ^[A-Za-z0-9.,:_\ -]+$ ]]; then
    reason="verification failed; inspect local journal"
  fi
  for state in "$overall" "$preflight" "$service" "$health" "$lab"; do
    if [[ ! "$state" =~ ^(PASS|FAIL|NOT_RUN)$ ]]; then
      printf 'verify-platform: invalid summary state\n' >&2
      return 1
    fi
  done
  sed -e "s|__OVERALL__|$overall|g" \
    -e "s|__PREFLIGHT__|$preflight|g" \
    -e "s|__SERVICE__|$service|g" \
    -e "s|__HEALTH__|$health|g" \
    -e "s|__MESSAGE__|$reason|g" \
    "$repo_root/deploy/openeuler/summary.template.json" > "$output_dir/summary.json"
}

if ! "$script_dir/preflight.sh" --json > "$output_dir/preflight.json"; then
  write_summary "FAIL" "FAIL" "NOT_RUN" "NOT_RUN" "NOT_RUN" "platform preflight failed"
  printf 'verify-platform: preflight failed; evidence retained in %s\n' "$output_rel" >&2
  exit 1
fi

for tool in curl sed grep systemctl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    write_summary "FAIL" "PASS" "NOT_RUN" "NOT_RUN" "NOT_RUN" "required local verifier tool missing"
    printf 'verify-platform: required tool missing: %s\n' "$tool" >&2
    exit 1
  fi
done
if ! systemctl is-active --quiet aegisrt.service; then
  write_summary "FAIL" "PASS" "FAIL" "NOT_RUN" "NOT_RUN" "aegisrt service is not active"
  printf 'verify-platform: aegisrt.service is not active\n' >&2
  exit 1
fi

delegate=$(systemctl show aegisrt.service --property=Delegate --value)
control_group=$(systemctl show aegisrt.service --property=ControlGroup --value)
if [[ "$control_group" != /* || "$control_group" == *..* ||
      ! "$control_group" =~ ^/[A-Za-z0-9_.@:/-]+$ ]]; then
  write_summary "FAIL" "PASS" "FAIL" "NOT_RUN" "NOT_RUN" "invalid service cgroup metadata"
  exit 1
fi
cgroup_dir="/sys/fs/cgroup${control_group}"
available=""
enabled=""
if [[ -r "$cgroup_dir/cgroup.controllers" ]]; then
  read -r available < "$cgroup_dir/cgroup.controllers" || true
fi
if [[ -r "$cgroup_dir/cgroup.subtree_control" ]]; then
  read -r enabled < "$cgroup_dir/cgroup.subtree_control" || true
fi
if [[ ! "$available" =~ ^[a-z0-9_\ ]*$ || ! "$enabled" =~ ^[+a-z0-9_\ ]*$ ]]; then
  write_summary "FAIL" "PASS" "FAIL" "NOT_RUN" "NOT_RUN" "invalid cgroup controller metadata"
  exit 1
fi
controllers_ok=true
for controller in cpu memory pids; do
  if [[ " $available " != *" $controller "* || " $enabled " != *" $controller "* ]]; then
    controllers_ok=false
  fi
done
manager_ok=false
manager_pid=""
if [[ -d "$cgroup_dir/manager" && -r "$cgroup_dir/manager/cgroup.procs" ]]; then
  read -r manager_pid < "$cgroup_dir/manager/cgroup.procs" || true
  if [[ "$manager_pid" =~ ^[1-9][0-9]*$ ]]; then
    manager_ok=true
  fi
fi
unset manager_pid
if [[ "$delegate" != "yes" || "$controllers_ok" != true || "$manager_ok" != true ]]; then
  delegate_ok=false
  if [[ "$delegate" == "yes" ]]; then delegate_ok=true; fi
  printf '{"schema_version":1,"active":true,"delegate":%s,' "$delegate_ok" \
    > "$output_dir/service.json"
  printf '"required_controllers_available":%s,' "$controllers_ok" \
    >> "$output_dir/service.json"
  printf '"required_controllers_enabled":%s,"manager_migrated":%s}\n' \
    "$controllers_ok" "$manager_ok" >> "$output_dir/service.json"
  write_summary "FAIL" "PASS" "FAIL" "NOT_RUN" "NOT_RUN" "cgroup delegation or manager migration missing"
  exit 1
fi
printf '{"schema_version":1,"active":true,"delegate":true,' > "$output_dir/service.json"
printf '"required_controllers_available":true,"required_controllers_enabled":true,' \
  >> "$output_dir/service.json"
printf '"manager_migrated":true}\n' >> "$output_dir/service.json"

base_url=http://127.0.0.1:8080
if ! curl --fail-with-body --silent --show-error --connect-timeout 2 \
  "$base_url/healthz" > /dev/null ||
   ! curl --fail-with-body --silent --show-error --connect-timeout 2 \
  "$base_url/readyz" > /dev/null; then
  write_summary "FAIL" "PASS" "PASS" "FAIL" "NOT_RUN" "daemon health or readiness check failed"
  exit 1
fi

write_summary "PASS" "PASS" "PASS" "PASS" "NOT_RUN" "platform service checks passed; application verification is separate"
chmod 0600 "$output_dir"/*.json
release_output
printf 'platform evidence written to %s\n' "$output_rel"
