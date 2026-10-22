#!/usr/bin/env bash
set -euo pipefail

mode="text"
case "${1:-}" in
  "") ;;
  --json) mode="json" ;;
  -h|--help)
    printf 'Usage: %s [--json]\n' "$0"
    exit 0
    ;;
  *)
    printf 'preflight: unsupported argument: %s\n' "$1" >&2
    exit 2
    ;;
esac

declare -a check_names=()
declare -a check_states=()
declare -a check_details=()
failed=0

record_check() {
  local name="$1" state="$2" detail="$3"
  check_names+=("$name")
  check_states+=("$state")
  check_details+=("$detail")
  if [[ "$state" != "PASS" ]]; then
    failed=1
  fi
}

json_string() {
  local value="$1"
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/ }
  value=${value//$'\r'/ }
  printf '"%s"' "$value"
}

os_id="unknown"
os_version="unknown"
if [[ -r /etc/os-release ]]; then
  while IFS='=' read -r key value; do
    value=${value#\"}
    value=${value%\"}
    case "$key" in
      ID) os_id="$value" ;;
      VERSION_ID) os_version="$value" ;;
    esac
  done < /etc/os-release
fi
if [[ "${os_id,,}" == "openeuler" ]]; then
  record_check "operating_system" "PASS" "openEuler ${os_version}"
else
  record_check "operating_system" "FAIL" "expected openEuler"
fi

if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
  record_check "systemd" "PASS" "systemd is PID 1 service manager"
else
  record_check "systemd" "FAIL" "systemd is unavailable"
fi

cgroup_type="unavailable"
if command -v stat >/dev/null 2>&1; then
  cgroup_type=$(stat -fc %T /sys/fs/cgroup 2>/dev/null || true)
fi
if [[ "$cgroup_type" == "cgroup2fs" && -r /sys/fs/cgroup/cgroup.controllers ]]; then
  record_check "cgroup_v2" "PASS" "unified cgroup v2 hierarchy"
else
  record_check "cgroup_v2" "FAIL" "cgroup2fs hierarchy is required"
fi

controllers=""
if [[ -r /sys/fs/cgroup/cgroup.controllers ]]; then
  read -r controllers < /sys/fs/cgroup/cgroup.controllers || true
fi
missing_controllers=()
for controller in cpu memory pids; do
  if [[ " $controllers " != *" $controller "* ]]; then
    missing_controllers+=("$controller")
  fi
done
if (( ${#missing_controllers[@]} == 0 )); then
  record_check "controllers" "PASS" "cpu memory pids available"
else
  record_check "controllers" "FAIL" "missing: ${missing_controllers[*]}"
fi

missing_psi=()
for pressure in cpu memory io; do
  if [[ ! -r "/proc/pressure/$pressure" ]]; then
    missing_psi+=("$pressure")
  fi
done
if (( ${#missing_psi[@]} == 0 )); then
  record_check "psi" "PASS" "cpu memory io PSI available"
else
  record_check "psi" "FAIL" "missing: ${missing_psi[*]}"
fi

missing_tools=()
for tool in go git sha256sum sed install; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    missing_tools+=("$tool")
  fi
done
if (( ${#missing_tools[@]} == 0 )); then
  record_check "build_tools" "PASS" "local build tools available"
else
  record_check "build_tools" "FAIL" "missing: ${missing_tools[*]}"
fi

go_version="unavailable"
go_supported=0
if command -v go >/dev/null 2>&1; then
  go_version=$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null || true)
  if [[ "$go_version" =~ ^go([0-9]+)\.([0-9]+)(\.[0-9]+)?$ ]]; then
    major=${BASH_REMATCH[1]}
    minor=${BASH_REMATCH[2]}
    if (( major > 1 || (major == 1 && minor >= 23) )); then
      go_supported=1
    fi
  fi
fi
if (( go_supported == 1 )); then
  record_check "go_version" "PASS" "$go_version (minimum 1.23)"
else
  record_check "go_version" "FAIL" "$go_version (minimum 1.23)"
fi

if [[ "$mode" == "json" ]]; then
  if (( failed == 0 )); then overall="PASS"; else overall="FAIL"; fi
  printf '{"schema_version":1,"overall":'
  json_string "$overall"
  printf ',"platform":{"id":'
  json_string "$os_id"
  printf ',"version_id":'
  json_string "$os_version"
  printf '},"checks":['
  for (( index=0; index<${#check_names[@]}; index++ )); do
    if (( index > 0 )); then printf ','; fi
    printf '{"name":'
    json_string "${check_names[$index]}"
    printf ',"status":'
    json_string "${check_states[$index]}"
    printf ',"detail":'
    json_string "${check_details[$index]}"
    printf '}'
  done
  printf ']}\n'
else
  for (( index=0; index<${#check_names[@]}; index++ )); do
    printf '%-16s %-4s %s\n' "${check_names[$index]}" "${check_states[$index]}" "${check_details[$index]}"
  done
fi

exit "$failed"
