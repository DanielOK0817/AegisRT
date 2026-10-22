"""Verify saved archives against their manifests and the baseline Git objects."""
import argparse
import hashlib
import json
import subprocess
import zipfile
from pathlib import Path


def require(value, message):
    if not value:
        raise ValueError(message)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--build-directory", type=Path, required=True)
    parser.add_argument("--compare-workspace", action="store_true")
    args = parser.parse_args()
    root = args.build_directory.resolve()
    repo = Path(__file__).resolve().parents[2]
    manifest = json.loads((root / "build.json").read_text(encoding="utf-8-sig"))
    adapters = json.loads((root / "adapter-source.json").read_text(encoding="utf-8-sig"))
    counts = {}
    for role in ("baseline", "current"):
        identity = json.loads((root / (role + "-source.json")).read_text(encoding="utf-8-sig"))
        canonical = "\n".join(entry["path"] + " " + entry["sha256"] for entry in identity["files"])
        require(hashlib.sha256(canonical.encode()).hexdigest() == identity["sha256"] == manifest[role + "_source_sha256"], "source manifest hash")
        archive_path = root / (role + "-build-input.zip")
        archive_info = next(item for item in manifest["archives"] if item["role"] == role)
        require(hashlib.sha256(archive_path.read_bytes()).hexdigest() == archive_info["sha256"], "archive hash")
        with zipfile.ZipFile(archive_path) as archive:
            for entry in identity["files"]:
                payload = archive.read(entry["path"])
                require(hashlib.sha256(payload).hexdigest() == entry["sha256"], "archived file hash: " + entry["path"])
                if role == "baseline":
                    original = subprocess.run(["git", "show", manifest["baseline_commit"] + ":" + entry["path"]],
                                              cwd=repo, check=True, capture_output=True).stdout
                    require(payload == original, "baseline differs from Git object: " + entry["path"])
                elif args.compare_workspace:
                    require(payload == (repo / entry["path"]).read_bytes(), "workspace changed: " + entry["path"])
            for entry in adapters["files"]:
                payload = archive.read("cmd/corecomparison/" + entry["path"].removesuffix(".tmpl"))
                require(hashlib.sha256(payload).hexdigest() == entry["sha256"], "adapter archive mismatch")
        counts[role] = len(identity["files"])
    result = {"passed": True, "source_archive_files_checked": counts, "baseline_matches_git_objects": True,
              "current_matches_workspace": args.compare_workspace,
              "check_script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
    with (root / "source-validation.json").open("x", encoding="utf-8", newline="\n") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
    print("Source archives verified against manifests, adapters and baseline Git objects.")


if __name__ == "__main__":
    main()
