#!/usr/bin/env python3
"""Safely merge an imported daily group-chat JSONL and its media files."""

import argparse
import filecmp
import json
from pathlib import Path
import shutil
import sys


def read_jsonl(path: Path) -> list[dict]:
    records = []
    with path.open(encoding="utf-8") as source:
        for line_number, line in enumerate(source, 1):
            if not line.strip():
                continue
            record = json.loads(line)
            if not str(record.get("id", "")).strip():
                raise ValueError(f"{path}:{line_number}: missing message id")
            records.append(record)
    return records


def media_paths(value: object) -> list[Path]:
    if not isinstance(value, str):
        return []
    return [Path(item.strip()) for item in value.split(",") if item.strip()]


def merge_records(local_records: list[dict], remote_records: list[dict]) -> tuple[list[dict], int]:
    by_id: dict[str, dict] = {}
    overlap = 0
    for record in local_records:
        by_id[str(record["id"])] = record
    for remote in remote_records:
        message_id = str(remote["id"])
        if message_id not in by_id:
            by_id[message_id] = remote
            continue
        overlap += 1
        local = by_id[message_id]
        merged = local | remote
        if not media_paths(merged.get("downloaded_media")) and media_paths(local.get("downloaded_media")):
            merged["downloaded_media"] = local["downloaded_media"]
            if local.get("has_image"):
                merged["has_image"] = True
        by_id[message_id] = merged
    records = list(by_id.values())
    records.sort(key=lambda item: (str(item.get("time", "")), str(item.get("id", ""))))
    return records, overlap


def copy_media(source_dir: Path, target_dir: Path) -> tuple[int, int]:
    target_dir.mkdir(parents=True, exist_ok=True)
    copied = 0
    identical = 0
    for source in sorted(source_dir.iterdir()):
        if not source.is_file():
            continue
        target = target_dir / source.name
        if target.exists():
            if not filecmp.cmp(source, target, shallow=False):
                raise ValueError(f"media filename conflict: {source.name}")
            identical += 1
            continue
        shutil.copy2(source, target)
        copied += 1
    return copied, identical


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--local-json", type=Path, required=True)
    parser.add_argument("--remote-json", type=Path, required=True)
    parser.add_argument("--media-source", type=Path, required=True)
    parser.add_argument("--media-target", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    local_records = read_jsonl(args.local_json)
    remote_records = read_jsonl(args.remote_json)
    merged, overlap = merge_records(local_records, remote_records)
    copied, identical = copy_media(args.media_source, args.media_target)

    missing_media = []
    for record in merged:
        for path in media_paths(record.get("downloaded_media")):
            if not path.is_file():
                missing_media.append(str(path))
    if missing_media:
        raise ValueError(f"{len(missing_media)} referenced media files are missing")

    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("w", encoding="utf-8") as target:
        for record in merged:
            target.write(json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n")
    args.output.chmod(0o644)
    print(json.dumps({
        "local_records": len(local_records),
        "remote_records": len(remote_records),
        "overlap_records": overlap,
        "merged_records": len(merged),
        "media_copied": copied,
        "media_identical": identical,
        "missing_media": 0,
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"merge failed: {error}", file=sys.stderr)
        raise SystemExit(1)
