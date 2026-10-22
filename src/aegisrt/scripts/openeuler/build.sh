#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
output_rel="dist/openeuler"

while (( $# > 0 )); do
  case "$1" in
    --output-dir)
      if (( $# < 2 )); then
        printf 'build: --output-dir requires a value\n' >&2
        exit 2
      fi
      output_rel="$2"
      shift 2
      ;;
    -h|--help)
      printf 'Usage: %s [--output-dir dist/NAME]\n' "$0"
      exit 0
      ;;
    *)
      printf 'build: unsupported argument: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [[ ! "$output_rel" =~ ^dist/[A-Za-z0-9._-]+$ || "$output_rel" == *..* ]]; then
  printf 'build: output must be one direct child of repository dist/\n' >&2
  exit 2
fi
if [[ -L "$repo_root/dist" ]]; then
  printf 'build: refusing symlinked dist directory\n' >&2
  exit 1
fi
output_dir="$repo_root/$output_rel"
mkdir -p -- "$output_dir"
if [[ -L "$output_dir" ]]; then
  printf 'build: refusing symlinked output directory\n' >&2
  exit 1
fi

for tool in go git sha256sum mktemp; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'build: required local tool is unavailable: %s\n' "$tool" >&2
    exit 1
  fi
done

go_version=$(GOTOOLCHAIN=local go env GOVERSION)
if [[ ! "$go_version" =~ ^go([0-9]+)\.([0-9]+)(\.[0-9]+)?$ ]]; then
  printf 'build: cannot parse local Go version\n' >&2
  exit 1
fi
major=${BASH_REMATCH[1]}
minor=${BASH_REMATCH[2]}
if (( major < 1 || (major == 1 && minor < 23) )); then
  printf 'build: Go 1.23 or newer is required\n' >&2
  exit 1
fi

commit="source-export"
dirty=false
if git -C "$repo_root" rev-parse --verify HEAD >/dev/null 2>&1; then
  commit=$(git -C "$repo_root" rev-parse --verify HEAD)
  if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=normal --ignore-submodules)" ]]; then
    dirty=true
  fi
fi
version=${commit:0:12}
if [[ "$dirty" == true ]]; then version+="-dirty"; fi

target_arch=$(GOTOOLCHAIN=local go env GOARCH)
if [[ ! "$target_arch" =~ ^[a-z0-9_]+$ ]]; then
  printf 'build: invalid target architecture\n' >&2
  exit 1
fi

temp_dir=$(mktemp -d "$output_dir/.build.XXXXXX")
cleanup() {
  rm -f -- "$temp_dir/aegisrt" "$temp_dir/aegisbench" \
    "$temp_dir/build-manifest.json" "$temp_dir/SHA256SUMS"
  rmdir -- "$temp_dir" 2>/dev/null || true
}
trap cleanup EXIT

build_binary() {
  local output="$1" package="$2"
  (
    cd -- "$repo_root"
    env CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" GOTOOLCHAIN=local GOFLAGS= \
      go build -trimpath -buildvcs=false -mod=readonly -ldflags "-X main.version=$version" \
      -o "$output" "$package"
  )
}

build_binary "$temp_dir/aegisrt" ./cmd/aegisrt
build_binary "$temp_dir/aegisbench" ./cmd/aegisbench
chmod 0755 "$temp_dir/aegisrt" "$temp_dir/aegisbench"

read -r runtime_hash _ < <(sha256sum "$temp_dir/aegisrt")
read -r benchmark_hash _ < <(sha256sum "$temp_dir/aegisbench")
if [[ ! "$runtime_hash" =~ ^[0-9a-f]{64}$ ||
      ! "$benchmark_hash" =~ ^[0-9a-f]{64}$ ]]; then
  printf 'build: invalid SHA-256 result\n' >&2
  exit 1
fi
printf '%s  aegisrt\n%s  aegisbench\n' \
  "$runtime_hash" "$benchmark_hash" > "$temp_dir/SHA256SUMS"
printf '{"schema_version":1,"source_commit":"%s","source_dirty":%s,' "$commit" "$dirty" \
  > "$temp_dir/build-manifest.json"
printf '"go_version":"%s","target_os":"linux","target_arch":"%s",' "$go_version" "$target_arch" \
  >> "$temp_dir/build-manifest.json"
printf '"binaries":{"aegisrt":{"sha256":"%s"},' "$runtime_hash" \
  >> "$temp_dir/build-manifest.json"
printf '"aegisbench":{"sha256":"%s"}}}\n' "$benchmark_hash" \
  >> "$temp_dir/build-manifest.json"
chmod 0644 "$temp_dir/build-manifest.json" "$temp_dir/SHA256SUMS"

mv -f -- "$temp_dir/aegisrt" "$output_dir/aegisrt"
mv -f -- "$temp_dir/aegisbench" "$output_dir/aegisbench"
mv -f -- "$temp_dir/build-manifest.json" "$output_dir/build-manifest.json"
mv -f -- "$temp_dir/SHA256SUMS" "$output_dir/SHA256SUMS"
printf 'built %s for linux/%s\n' "$output_rel" "$target_arch"
