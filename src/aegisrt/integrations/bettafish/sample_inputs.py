"""Validate and freeze fresh user samples without old reports or database writes."""
from __future__ import annotations

import argparse
import copy
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
from urllib.parse import urlsplit
import uuid

SCHEMA = "bettafish.sample-input/v1"
MAX_COMMENTS = 200
MAX_COMMENT_CHARACTERS = 2000
MAX_TOTAL_CHARACTERS = 40000
MAX_INPUT_BYTES = 1000000


class SampleInputError(ValueError):
    pass


def _json_bytes(value):
    try:
        return (json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode("utf-8")
    except (TypeError, ValueError, RecursionError, UnicodeError) as exc:
        raise SampleInputError("sample metadata must contain finite JSON values") from exc


def _unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise SampleInputError("duplicate JSON object field")
        value[key] = item
    return value


def _read(path, byte_limit=MAX_INPUT_BYTES):
    path = Path(path)
    if not path.is_file() or path.stat().st_size > byte_limit:
        raise SampleInputError("sample input file is missing or too large")
    raw = path.read_bytes()
    if len(raw) > byte_limit:
        raise SampleInputError("sample input file is too large")
    try:
        value = json.loads(raw.decode("utf-8-sig"), object_pairs_hook=_unique_object,
                           parse_constant=lambda _value: (_ for _ in ()).throw(SampleInputError("non-finite JSON value")))
    except (UnicodeError, json.JSONDecodeError, RecursionError) as exc:
        raise SampleInputError("sample input must be complete UTF-8 JSON") from exc
    return value, hashlib.sha256(raw).hexdigest()


def _limit(value, ceiling, name):
    if type(value) is not int or not 1 <= value <= ceiling:
        raise SampleInputError("invalid " + name)
    return value


def _source_url(value):
    if not isinstance(value, str) or not value or len(value) > 2000 or any(char.isspace() or ord(char) < 32 for char in value):
        raise SampleInputError("comment source_url must be a complete HTTP(S) URL")
    try:
        parsed = urlsplit(value)
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username is not None or parsed.password is not None:
            raise ValueError("invalid source")
        parsed.port
    except ValueError as exc:
        raise SampleInputError("comment source_url must be a complete HTTP(S) URL") from exc
    return value


def validate_sample(topic, comments, provenance, *, max_comments=MAX_COMMENTS,
                    max_comment_characters=MAX_COMMENT_CHARACTERS, max_total_characters=MAX_TOTAL_CHARACTERS):
    """Return a detached validated sample; perform no file/database writes."""
    limits = {"max_comments": _limit(max_comments, MAX_COMMENTS, "max_comments"),
              "max_comment_characters": _limit(max_comment_characters, MAX_COMMENT_CHARACTERS, "max_comment_characters"),
              "max_total_characters": _limit(max_total_characters, MAX_TOTAL_CHARACTERS, "max_total_characters")}
    if not isinstance(topic, str) or not topic or topic != topic.strip() or len(topic) > 300 or any(ord(char) < 32 for char in topic):
        raise SampleInputError("topic must be nonempty text of at most 300 characters without surrounding whitespace")
    if not isinstance(provenance, dict) or provenance.get("topic") != topic:
        raise SampleInputError("provenance topic must match the submitted topic exactly")
    if not isinstance(comments, list) or not 2 <= len(comments) <= limits["max_comments"]:
        raise SampleInputError("sample must contain 2 to max_comments original comments")
    seen, sources, total = set(), [], 0
    for row in comments:
        if not isinstance(row, dict):
            raise SampleInputError("each original comment must be an object")
        identifier = row.get("comment_id")
        if not (isinstance(identifier, str) or type(identifier) is int and identifier > 0):
            raise SampleInputError("comment_id must be nonempty text or a positive integer")
        identifier = str(identifier)
        if not identifier or len(identifier) > 128 or any(char.isspace() or ord(char) < 32 for char in identifier) or identifier in seen:
            raise SampleInputError("duplicate or invalid comment_id")
        body = row.get("content")
        if not isinstance(body, str) or not body.strip() or len(body) > limits["max_comment_characters"] or any(ord(char) < 32 and char not in "\n\r\t" for char in body):
            raise SampleInputError("empty, invalid or too long original comment content")
        if "topic" in row and row["topic"] != topic:
            raise SampleInputError("comment topic differs from the submitted topic")
        source = _source_url(row.get("source_url"))
        if source not in sources:
            sources.append(source)
        if "like_count" in row and (type(row["like_count"]) is not int or row["like_count"] < 0):
            raise SampleInputError("like_count must be a nonnegative integer")
        for field in ("nickname", "platform"):
            if field in row and (not isinstance(row[field], str) or len(row[field]) > 200):
                raise SampleInputError("invalid comment " + field)
        if "publish_time" in row and row["publish_time"] is not None:
            if not isinstance(row["publish_time"], str):
                raise SampleInputError("publish_time must be an ISO timestamp")
            try:
                datetime.fromisoformat(row["publish_time"].replace("Z", "+00:00"))
            except ValueError as exc:
                raise SampleInputError("publish_time must be an ISO timestamp") from exc
        if "create_time" in row and row["create_time"] is not None:
            if type(row["create_time"]) is not int or not 0 < row["create_time"] < 253402300800:
                raise SampleInputError("create_time must be a positive Unix timestamp")
        seen.add(identifier)
        total += len(body)
    if total > limits["max_total_characters"]:
        raise SampleInputError("sample total content exceeds max_total_characters")
    declared = provenance.get("source_urls")
    if declared is not None:
        if not isinstance(declared, list) or not declared or len(declared) > MAX_COMMENTS:
            raise SampleInputError("provenance source_urls must be a bounded list")
        if not set(sources).issubset({_source_url(value) for value in declared}):
            raise SampleInputError("provenance source_urls do not cover the original comments")
    for name, maximum in (("sampling", 2000), ("note", 5000), ("finished_at", 64)):
        if name in provenance and (not isinstance(provenance[name], str) or len(provenance[name]) > maximum):
            raise SampleInputError("invalid provenance " + name)
    if provenance.get("finished_at"):
        try:
            datetime.fromisoformat(provenance["finished_at"].replace("Z", "+00:00"))
        except ValueError as exc:
            raise SampleInputError("provenance finished_at must be an ISO timestamp") from exc
    if len(_json_bytes(comments)) > MAX_INPUT_BYTES or len(_json_bytes(provenance)) > MAX_INPUT_BYTES:
        raise SampleInputError("sample metadata is too large")
    return {"topic": topic, "comments": copy.deepcopy(comments), "provenance": copy.deepcopy(provenance),
            "sample_count": len(comments), "total_characters": total, "source_urls": sources, "limits": limits}


def _freeze(validated, output_dir, source_sha256=None):
    target = Path(output_dir).resolve()
    try:
        target.mkdir(parents=True, exist_ok=False)
    except FileExistsError as exc:
        raise SampleInputError("fresh input directory already exists") from exc
    records = {}
    for name in ("comments", "provenance"):
        raw = _json_bytes(validated[name])
        path = target / (name + ".json")
        path.write_bytes(raw)
        records[name] = {"path": path.name, "sha256": hashlib.sha256(raw).hexdigest()}
    manifest = {"schema": SCHEMA, "input_id": str(uuid.uuid4()), "topic": validated["topic"],
                "sample_source": "file", "inputs": {}, "files": records,
                "comments_sha256": records["comments"]["sha256"], "provenance_sha256": records["provenance"]["sha256"],
                "sample_count": validated["sample_count"], "total_characters": validated["total_characters"],
                "source_urls": validated["source_urls"], "limits": validated["limits"],
                "frozen_at": datetime.now(timezone.utc).isoformat()}
    if source_sha256 is not None:
        manifest["source_file_sha256"] = source_sha256
    raw = _json_bytes(manifest)
    (target / "manifest.json").write_bytes(raw)  # Publish the manifest only after both data files exist.
    return {"input_dir": str(target), "sample_count": validated["sample_count"],
            "total_characters": validated["total_characters"], "manifest_sha256": hashlib.sha256(raw).hexdigest()}


def freeze_sample_values(topic, comments, provenance, output_dir, **limits):
    return _freeze(validate_sample(topic, comments, provenance, **limits), output_dir)


def freeze_sample_inputs(topic, comments_path, provenance_path, output_dir, **limits):
    comments, comments_sha = _read(comments_path)
    provenance, provenance_sha = _read(provenance_path)
    return _freeze(validate_sample(topic, comments, provenance, **limits), output_dir,
                   {"comments": comments_sha, "provenance": provenance_sha})


def load_sample_inputs(input_dir, topic):
    """Verify a relocatable fresh manifest and both exact sample file digests."""
    root = Path(input_dir).resolve()
    manifest, manifest_sha = _read(root / "manifest.json")
    if not isinstance(manifest, dict) or manifest.get("schema") != SCHEMA or manifest.get("sample_source") != "file" or manifest.get("topic") != topic or manifest.get("inputs") != {}:
        raise SampleInputError("fresh sample manifest identity mismatch")
    try:
        if str(uuid.UUID(manifest["input_id"])) != manifest["input_id"]:
            raise ValueError("noncanonical input id")
    except (KeyError, TypeError, ValueError, AttributeError) as exc:
        raise SampleInputError("invalid fresh input_id") from exc
    if not isinstance(manifest.get("files"), dict):
        raise SampleInputError("invalid fresh sample file references")
    files, data = {}, {}
    for name in ("comments", "provenance"):
        record = manifest.get("files", {}).get(name, {})
        if not isinstance(record, dict) or record.get("path") != name + ".json" or not isinstance(record.get("sha256"), str) or not re.fullmatch(r"[a-f0-9]{64}", record["sha256"]):
            raise SampleInputError("invalid fresh sample file reference")
        path = (root / record["path"]).resolve()
        if not path.is_relative_to(root):
            raise SampleInputError("fresh sample file escapes its input directory")
        value, sha = _read(path)
        if sha != record["sha256"] or sha != manifest.get(name + "_sha256"):
            raise SampleInputError("fresh sample checksum mismatch")
        files[name] = {"path": str(path), "sha256": sha}
        data[name] = value
    declared_limits = manifest.get("limits")
    if not isinstance(declared_limits, dict) or set(declared_limits) != {"max_comments", "max_comment_characters", "max_total_characters"}:
        raise SampleInputError("invalid fresh sample limits")
    validated = validate_sample(topic, data["comments"], data["provenance"], **declared_limits)
    if any(type(manifest.get(key)) is not int or manifest[key] != validated[key] for key in ("sample_count", "total_characters")) or manifest.get("source_urls") != validated["source_urls"]:
        raise SampleInputError("fresh sample metadata differs from its files")
    return {**validated, "manifest": manifest, "input_dir": str(root),
            "files": {**files, "manifest": {"path": str(root / "manifest.json"), "sha256": manifest_sha}}}


def insight_record_values(comments, topic):
    """Construct upstream QueryResult keyword values without querying a database."""
    return [{"platform": row.get("platform") or urlsplit(row["source_url"]).hostname,
             "content_type": "comment", "title_or_content": row["content"], "author_nickname": row.get("nickname"),
             "url": row["source_url"], "publish_time": (datetime.fromisoformat(row["publish_time"].replace("Z", "+00:00"))
                 if row.get("publish_time") else datetime.fromtimestamp(row["create_time"], timezone.utc) if row.get("create_time") else None),
             "engagement": {"likes": row.get("like_count", 0)}, "source_keyword": topic,
             "hotness_score": float(row.get("like_count", 0)), "source_table": "file_sample_comments", "source_id": str(row["comment_id"])} for row in comments]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--topic", required=True)
    parser.add_argument("--comments", type=Path, required=True)
    parser.add_argument("--provenance", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = freeze_sample_inputs(args.topic, args.comments, args.provenance, args.output_dir)
    except (OSError, SampleInputError) as exc:
        parser.exit(1, "Sample freeze failed: " + str(exc) + "\n")
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
