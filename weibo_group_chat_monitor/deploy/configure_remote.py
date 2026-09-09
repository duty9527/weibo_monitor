#!/usr/bin/env python3
"""Apply deployment-only config changes without touching existing secrets."""

from pathlib import Path
import re


ROOT = Path("/home/duty/weibo_cron")


def replace_scalar_in_section(text: str, section: str, key: str, value: str) -> str:
    lines = text.splitlines(keepends=True)
    in_section = False
    replaced = False
    for index, line in enumerate(lines):
        if re.match(rf"^{re.escape(section)}:\s*(?:#.*)?$", line.rstrip("\r\n")):
            in_section = True
            continue
        if in_section and line.strip() and not line[0].isspace():
            in_section = False
        if in_section:
            match = re.match(rf"^(\s+{re.escape(key)}:\s*).*$", line.rstrip("\r\n"))
            if match:
                newline = "\r\n" if line.endswith("\r\n") else "\n"
                lines[index] = f"{match.group(1)}{value}{newline}"
                replaced = True
                break
    if not replaced:
        raise RuntimeError(f"missing {section}.{key}")
    return "".join(lines)


def replace_top_level_section(text: str, section: str, replacement: str) -> str:
    lines = text.splitlines(keepends=True)
    start = None
    end = None
    for index, line in enumerate(lines):
        if re.match(rf"^{re.escape(section)}:\s*(?:#.*)?$", line.rstrip("\r\n")):
            start = index
            break
    if start is not None:
        end = len(lines)
        for index in range(start + 1, len(lines)):
            line = lines[index]
            if line.strip() and not line[0].isspace() and not line.lstrip().startswith("#"):
                end = index
                break
        del lines[start:end]
    while lines and not lines[-1].strip():
        lines.pop()
    return "".join(lines) + "\n\n" + replacement.rstrip() + "\n"


weibo_path = ROOT / "config.weibo.yaml"
weibo_text = weibo_path.read_text()
weibo_text = replace_scalar_in_section(weibo_text, "log", "level", "error")
weibo_text = re.sub(
    r"(?m)^(\s*playwright_refresh_hours:\s*).*$", r"\g<1>0", weibo_text, count=1
)
weibo_path.write_text(weibo_text)

group_path = ROOT / "config.groupchat.yaml"
group_text = group_path.read_text()
group_text = replace_scalar_in_section(group_text, "log", "level", "error")
group_text = replace_top_level_section(
    group_text,
    "subscription",
    """subscription:
  cookie_cache_file: "groupchat_cookies.json"
  event_log_file: "groupchat_events"
  event_log_retention_days: 7
  notification_queue_file: "groupchat_notification_outbox.json"
  processed_state_file: "groupchat_subscription_state.json"
  auth_refresh_state_file: "groupchat_auth_refresh_state.json"
  runtime_alert_state_file: "groupchat_runtime_alert_state.json"
  login_timeout_seconds: 180
  retry_delay_seconds: 5
  connect_timeout_seconds: 90
  auth_check_interval_hours: 6
  proactive_refresh_before_expiry_hours: 96
  backfill_enabled: true
  backfill_max_pages: 200
  backfill_on_first_start: false
  tls_insecure_skip_verify: false""",
)
group_path.write_text(group_text)

weibo_path.chmod(0o600)
group_path.chmod(0o600)
