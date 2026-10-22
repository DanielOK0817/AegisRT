#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
source_rel="dist/openeuler"
registry_source=""

while (( $# > 0 )); do
  case "$1" in
    --agents-file)
      [[ $# -ge 2 ]] || { printf 'install: --agents-file requires a value\n' >&2; exit 2; }
      registry_source="$2"; shift 2 ;;
    --source-dir)
      if (( $# < 2 )); then
        printf 'install: --source-dir requires a value\n' >&2
        exit 2
      fi
      source_rel="$2"
      shift 2
      ;;
    -h|--help)
      printf 'Usage: sudo %s [--source-dir dist/NAME] [--agents-file /absolute/agents.json]\n' "$0"
      exit 0
      ;;
    *)
      printf 'install: unsupported argument: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if (( EUID != 0 )); then
  printf 'install: run explicitly as root\n' >&2
  exit 1
fi

if [[ ! "$source_rel" =~ ^dist/[A-Za-z0-9._-]+$ || "$source_rel" == *..* ]]; then
  printf 'install: source must be one direct child of repository dist/\n' >&2
  exit 2
fi
source_dir="$repo_root/$source_rel"
for file in aegisrt aegisbench SHA256SUMS build-manifest.json; do
  if [[ ! -f "$source_dir/$file" || -L "$source_dir/$file" ]]; then
    printf 'install: missing or unsafe build artifact: %s\n' "$file" >&2
    exit 1
  fi
done

mapfile -t checksum_lines < "$source_dir/SHA256SUMS"
if (( ${#checksum_lines[@]} != 2 )) ||
   [[ ! "${checksum_lines[0]}" =~ ^[0-9a-f]{64}[[:space:]][[:space:]]aegisrt$ ]] ||
   [[ ! "${checksum_lines[1]}" =~ ^[0-9a-f]{64}[[:space:]][[:space:]]aegisbench$ ]]; then
  printf 'install: SHA256SUMS has an unexpected shape\n' >&2
  exit 1
fi
(
  cd -- "$source_dir"
  sha256sum --check --strict SHA256SUMS
)

binary_dir=/usr/local/libexec/aegisrt
benchmark_binary=/usr/local/libexec/aegisrt/aegisbench
config_dir=/etc/aegisrt
state_dir=/var/lib/aegisrt
unit_file=/usr/lib/systemd/system/aegisrt.service
sysusers_file=/usr/lib/sysusers.d/aegisrt.conf
benchmark_unit_file=/usr/lib/systemd/system/aegisrt-bench-f4-smoke.service
resource_benchmark_unit_file=/usr/lib/systemd/system/aegisrt-bench-resource-smoke.service
benchmark_sysusers_file=/usr/lib/sysusers.d/aegisrt-bench.conf
token_file="$config_dir/api.token"
agents_file="$config_dir/agents.json"

if [[ ! -e "$agents_file" && ( "$registry_source" != /* || ! -f "$registry_source" || -L "$registry_source" ) ]]; then
  printf 'install: provide a regular absolute --agents-file for first installation\n' >&2
  exit 2
fi

for path in "$binary_dir" "$config_dir" "$state_dir"; do
  if [[ -L "$path" ]]; then
    printf 'install: refusing symlinked managed directory: %s\n' "$path" >&2
    exit 1
  fi
done
for path in "$unit_file" "$sysusers_file" "$benchmark_unit_file" "$resource_benchmark_unit_file" \
  "$benchmark_sysusers_file" "$token_file" "$agents_file"; do
  if [[ -L "$path" ]]; then
    printf 'install: refusing symlinked managed file: %s\n' "$path" >&2
    exit 1
  fi
done

install -D -m 0644 "$repo_root/deploy/openeuler/aegisrt.sysusers" "$sysusers_file"
install -D -m 0644 "$repo_root/deploy/openeuler/aegisrt-bench.sysusers" \
  "$benchmark_sysusers_file"
systemd-sysusers "$sysusers_file" "$benchmark_sysusers_file"
install -d -m 0755 -o root -g root "$binary_dir"
install -d -m 0700 -o aegisrt -g aegisrt "$config_dir" "$state_dir"
install -m 0755 -o root -g root "$source_dir/aegisrt" "$binary_dir/aegisrt"
install -m 0755 -o root -g root "$source_dir/aegisbench" "$benchmark_binary"
install -D -m 0644 -o root -g root "$repo_root/deploy/openeuler/aegisrt.service" "$unit_file"
install -D -m 0644 -o root -g root \
  "$repo_root/deploy/openeuler/aegisrt-bench-f4-smoke.service" "$benchmark_unit_file"
install -D -m 0644 -o root -g root \
  "$repo_root/deploy/openeuler/aegisrt-bench-resource-smoke.service" "$resource_benchmark_unit_file"

temp_token=""
temp_agents=""
cleanup() {
  if [[ -n "$temp_token" ]]; then rm -f -- "$temp_token"; fi
  if [[ -n "$temp_agents" ]]; then rm -f -- "$temp_agents"; fi
}
trap cleanup EXIT

if [[ -e "$token_file" ]]; then
  if [[ ! -f "$token_file" ]]; then
    printf 'install: existing API token is not a regular file\n' >&2
    exit 1
  fi
  existing_token=$(tr -d '\r\n' < "$token_file")
  if [[ ! "$existing_token" =~ ^[A-Za-z0-9._~+/=-]{32,512}$ ]]; then
    printf 'install: existing API token has an invalid shape\n' >&2
    exit 1
  fi
  unset existing_token
else
  temp_token=$(mktemp "$config_dir/.api.token.XXXXXX")
  token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  if [[ ! "$token" =~ ^[0-9a-f]{64}$ ]]; then
    printf 'install: secure token generation failed\n' >&2
    exit 1
  fi
  printf '%s\n' "$token" > "$temp_token"
  unset token
  chown aegisrt:aegisrt "$temp_token"
  chmod 0600 "$temp_token"
  mv -- "$temp_token" "$token_file"
  temp_token=""
fi
chown aegisrt:aegisrt "$token_file"
chmod 0600 "$token_file"

if [[ ! -e "$agents_file" ]]; then
  temp_agents=$(mktemp "$config_dir/.agents.json.XXXXXX")
  cp -- "$registry_source" "$temp_agents"
  chown aegisrt:aegisrt "$temp_agents"
  chmod 0600 "$temp_agents"
  mv -- "$temp_agents" "$agents_file"
  temp_agents=""
elif [[ ! -f "$agents_file" ]]; then
  printf 'install: existing Agent registry is not a regular file\n' >&2
  exit 1
fi
chown aegisrt:aegisrt "$agents_file"
chmod 0600 "$agents_file"

systemctl daemon-reload
printf 'AegisRT installed; service was not enabled or started.\n'
printf 'Review /etc/aegisrt/agents.json, then run: systemctl enable --now aegisrt.service\n'
printf 'The static F4 smoke benchmark unit was installed but not started.\n'
printf 'The static resource pressure smoke benchmark unit was installed but not started.\n'
