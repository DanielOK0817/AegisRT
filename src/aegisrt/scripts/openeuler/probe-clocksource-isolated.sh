#!/usr/bin/env bash
set -euo pipefail
test "$(id -u)" -eq 0 || { echo 'Root authentication is required.' >&2; exit 1; }
clock_dir=/sys/devices/system/clocksource/clocksource0
original=$(cat "$clock_dir/current_clocksource")
case " $(cat "$clock_dir/available_clocksource") " in *' kvm-clock '*) ;; *) echo 'kvm-clock is not available'; exit 1;; esac
output=$(mktemp -d /tmp/aegisrt-clock-probe-XXXXXXXX)
chmod 0755 "$output"
restore() { printf '%s\n' "$original" > "$clock_dir/current_clocksource"; cat "$clock_dir/current_clocksource" > "$output/restored.txt"; }
trap restore EXIT
printf '%s\n' "$original" > "$output/original.txt"
printf '%s\n' kvm-clock > "$clock_dir/current_clocksource"
cat "$clock_dir/current_clocksource" > "$output/selected.txt"
python3 -u - <<'PY' | tee "$output/samples.jsonl"
import json,time
for i in range(90):
    print(json.dumps({'index':i,'wall':time.time(),'monotonic':time.monotonic()}),flush=True)
    time.sleep(1)
PY
echo "Evidence: $output"
