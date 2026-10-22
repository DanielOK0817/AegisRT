#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
input_rel=""
output_rel=""

while (( $# > 0 )); do
  case "$1" in
    --input-dir)
      [[ $# -ge 2 ]] || { printf 'anonymize: --input-dir requires a value\n' >&2; exit 2; }
      input_rel="$2"; shift 2 ;;
    --output-dir)
      [[ $# -ge 2 ]] || { printf 'anonymize: --output-dir requires a value\n' >&2; exit 2; }
      output_rel="$2"; shift 2 ;;
    -h|--help)
      printf 'Usage: %s --input-dir experiments/raw/openeuler/NAME --output-dir experiments/processed/openeuler/NAME\n' "$0"
      exit 0 ;;
    *) printf 'anonymize: unsupported argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

if [[ ! "$input_rel" =~ ^experiments/raw/openeuler/[A-Za-z0-9._-]+$ || "$input_rel" == *..* ]]; then
  printf 'anonymize: input must be one run directory below experiments/raw/openeuler\n' >&2
  exit 2
fi
if [[ ! "$output_rel" =~ ^experiments/processed/openeuler/[A-Za-z0-9._-]+$ || "$output_rel" == *..* ]]; then
  printf 'anonymize: output must be one bundle below experiments/processed/openeuler\n' >&2
  exit 2
fi
input_dir="$repo_root/$input_rel"
output_dir="$repo_root/$output_rel"
if [[ ! -d "$input_dir" || -L "$input_dir" || -e "$output_dir" ]]; then
  printf 'anonymize: refusing unsafe input or existing output\n' >&2
  exit 1
fi

files=(preflight.json service.json summary.json)
for file in "${files[@]}"; do
  path="$input_dir/$file"
  if [[ ! -f "$path" || -L "$path" ]] || ! grep -q '^{' "$path"; then
    printf 'anonymize: refusing to publish missing or malformed %s\n' "$file" >&2
    exit 1
  fi
done

sensitive='Authorization|Bearer|password|secret|api[_-]?key|PRIVATE KEY|BEGIN [A-Z ]+ KEY|/home/|/root/|\\Users\\|[[:alnum:]._%+-]+@[[:alnum:].-]+\.[[:alpha:]]{2,}'
if grep -Eiq "$sensitive" "${files[@]/#/$input_dir/}"; then
  printf 'anonymize: refusing to publish evidence containing a sensitive pattern\n' >&2
  exit 1
fi
if grep -Eiq '(/etc/|/var/|/usr/|/tmp/|[0-9]{1,3}(\.[0-9]{1,3}){3})' "${files[@]/#/$input_dir/}"; then
  printf 'anonymize: refusing to publish evidence containing a host path or address\n' >&2
  exit 1
fi

output_parent=$(dirname -- "$output_dir")
mkdir -p -- "$output_parent"
if [[ -L "$repo_root/experiments" || -L "$repo_root/experiments/processed" || -L "$output_parent" ]]; then
  printf 'anonymize: refusing symlinked output parent\n' >&2
  exit 1
fi
temp_dir=$(mktemp -d "$output_parent/.bundle.XXXXXX")
cleanup() {
  if [[ -n "$temp_dir" ]]; then
    for file in "${files[@]}" SHA256SUMS; do rm -f -- "$temp_dir/$file"; done
    rmdir -- "$temp_dir" 2>/dev/null || true
  fi
}
trap cleanup EXIT
for file in "${files[@]}"; do
  cp -- "$input_dir/$file" "$temp_dir/$file"
  chmod 0644 "$temp_dir/$file"
done
(
  cd -- "$temp_dir"
  sha256sum "${files[@]}" > SHA256SUMS
)
chmod 0644 "$temp_dir/SHA256SUMS"
chmod 0755 "$temp_dir"
mv -- "$temp_dir" "$output_dir"
temp_dir=""
printf 'anonymous bundle written to %s\n' "$output_rel"
