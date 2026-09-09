#!/usr/bin/env python3
"""Enable the durable group-chat Telegram outbox without changing secrets."""

import argparse
from pathlib import Path
import re


def section_bounds(lines: list[str], section: str) -> tuple[int, int]:
    start = next(
        (index for index, line in enumerate(lines) if re.match(rf"^{re.escape(section)}:\s*(?:#.*)?$", line.rstrip("\r\n"))),
        None,
    )
    if start is None:
        raise RuntimeError(f"missing YAML section: {section}")
    end = len(lines)
    for index in range(start + 1, len(lines)):
        if lines[index].strip() and not lines[index][0].isspace() and not lines[index].lstrip().startswith("#"):
            end = index
            break
    return start, end


def upsert_scalar(text: str, section: str, key: str, value: str) -> str:
    lines = text.splitlines(keepends=True)
    start, end = section_bounds(lines, section)
    pattern = re.compile(rf"^(\s+){re.escape(key)}:\s*.*$")
    for index in range(start + 1, end):
        match = pattern.match(lines[index].rstrip("\r\n"))
        if match:
            lines[index] = f'{match.group(1)}{key}: "{value}"\n'
            return "".join(lines)
    lines.insert(end, f'  {key}: "{value}"\n')
    return "".join(lines)


def upsert_list_value(text: str, section: str, key: str, value: str) -> str:
    lines = text.splitlines(keepends=True)
    start, end = section_bounds(lines, section)
    key_pattern = re.compile(rf"^(\s+){re.escape(key)}:\s*(?:\[\s*\])?\s*(?:#.*)?$")
    key_index = None
    indent = "  "
    for index in range(start + 1, end):
        match = key_pattern.match(lines[index].rstrip("\r\n"))
        if match:
            key_index = index
            indent = match.group(1)
            break
    if key_index is None:
        lines.insert(end, f"  {key}:\n    - \"{value}\"\n")
        return "".join(lines)

    item_indent = indent + "  "
    list_end = key_index + 1
    existing = []
    while list_end < end:
        line = lines[list_end]
        if not line.strip() or line.lstrip().startswith("#"):
            list_end += 1
            continue
        leading = len(line) - len(line.lstrip())
        if leading <= len(indent):
            break
        match = re.match(r"^\s*-\s*[\"']?([^\"'#\s]+)[\"']?", line)
        if match:
            existing.append(match.group(1))
        list_end += 1
    if value not in existing:
        lines.insert(list_end, f'{item_indent}- "{value}"\n')
    if ": []" in lines[key_index]:
        lines[key_index] = f"{indent}{key}:\n"
    return "".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--uid", required=True)
    args = parser.parse_args()

    text = args.config.read_text(encoding="utf-8")
    text = upsert_scalar(text, "subscription", "notification_queue_file", "groupchat_notification_outbox.json")
    text = upsert_list_value(text, "filters", "target_sender_uids", args.uid.strip())
    args.config.write_text(text, encoding="utf-8")
    args.config.chmod(0o600)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
