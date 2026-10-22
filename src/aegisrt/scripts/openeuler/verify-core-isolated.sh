#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
if (( $# != 0 || EUID != 0 )); then
  printf 'Usage: sudo bash %s\n' "$0" >&2
  exit 2
fi
if [[ ! -f "$repo_root/.m4-f4-current" || -L "$repo_root/.m4-f4-current" ]]; then
  printf 'Prepare F4 binaries as the ordinary source owner before running this entry.\n' >&2
  exit 1
fi
read -r build_name < "$repo_root/.m4-f4-current"
if [[ ! "$build_name" =~ ^\.m4-f4-build-[A-Za-z0-9]{8}$ || ! -d "$repo_root/$build_name" || -L "$repo_root/$build_name" ]]; then
  printf 'The prepared F4 build directory is invalid.\n' >&2
  exit 1
fi
for name in aegisrt aegisbench daemon-build.txt harness-build.txt base-commit.txt source-files.sha256 SHA256SUMS; do
  if [[ ! -f "$repo_root/$build_name/$name" || -L "$repo_root/$build_name/$name" ]]; then
    printf 'The prepared F4 build is incomplete.\n' >&2
    exit 1
  fi
done
(cd -- "$repo_root/$build_name" && sha256sum --check --status SHA256SUMS)
evidence_uid=$(stat -c %u "$repo_root")
evidence_gid=$(stat -c %g "$repo_root")
if [[ ! "$evidence_uid" =~ ^[1-9][0-9]*$ || ! "$evidence_gid" =~ ^[1-9][0-9]*$ ]]; then
  printf 'The source owner must be an ordinary user.\n' >&2
  exit 1
fi
for name in verify-resource-isolated.sh verify-fault-isolated.sh; do
  test -r "$script_dir/$name"
done
command -v systemd-run >/dev/null
command -v setsid >/dev/null
# These values only return evidence ownership when entered through su rather
# than sudo; they are not an authentication mechanism or permission grant.
SUDO_UID="$evidence_uid" SUDO_GID="$evidence_gid" bash "$script_dir/verify-resource-isolated.sh"
bash "$script_dir/verify-fault-isolated.sh"
