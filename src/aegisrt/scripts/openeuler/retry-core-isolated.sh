#!/usr/bin/env bash
set -euo pipefail

# A foreground, bounded root interaction; no background agent or command queue.
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
if (( $# != 0 || EUID != 0 )); then
  printf 'Usage: run this script as root without arguments.\n' >&2
  exit 2
fi
entry="$script_dir/recover-systemd-isolated.sh"
test -f "$entry" && test ! -L "$entry"
for attempt in 1 2 3; do
  status=0
  printf 'Isolated verification attempt %d of 3.\n' "$attempt"
  bash "$entry" || status=$?
  printf 'Attempt exit code: %d\n' "$status"
  if (( status == 0 )); then exit 0; fi
  if (( attempt == 3 )); then exit "$status"; fi
  answer=""
  if ! IFS= read -r -t 300 -p 'Press Enter to retry the same isolated entry, or q to exit (5 minute limit): ' answer; then
    printf '\nInteraction timed out or closed.\n'
    exit "$status"
  fi
  if [[ "$answer" != "" ]]; then exit "$status"; fi
done
