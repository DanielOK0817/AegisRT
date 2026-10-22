"""Exercise rejection checks against a saved successful trace."""
import argparse
import copy
import hashlib
import json
from pathlib import Path

from verify import verify_trial


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-directory", type=Path, required=True)
    args = parser.parse_args()
    trial = json.loads((args.run_directory / "C.json").read_text(encoding="utf-8"))["trials"][0]
    mutations = [
        ("oversized_allocation", lambda value: value["events"][0]["allocated"].__setitem__("cpu_milli", 9999)),
        ("incorrect_settlement", lambda value: next(event for event in value["events"] if event["kind"] == "finish").__setitem__("accounted_charge", 0)),
        ("missing_finish", lambda value: value["events"].pop()),
        ("incorrect_metric", lambda value: value["metrics"].__setitem__("makespan_ms", 0)),
    ]
    rejected = []
    for name, mutate in mutations:
        changed = copy.deepcopy(trial)
        mutate(changed)
        try:
            verify_trial(changed, "C")
        except ValueError:
            rejected.append(name)
        else:
            raise ValueError("invalid trace was accepted: " + name)
    result = {"passed": True, "rejected_mutations": rejected,
              "check_script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
    with (args.run_directory / "negative-checks.json").open("x", encoding="utf-8", newline="\n") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
    print("Negative checks passed:", ", ".join(rejected))


if __name__ == "__main__":
    main()
