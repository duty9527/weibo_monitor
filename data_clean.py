#!/usr/bin/env python3
"""
微博群聊数据清洗脚本

输入: clean_history.jsonl
输出: cleaned_data.jsonl + 控制台清洗报告
"""

import json
import re
from collections import Counter
from datetime import datetime

INPUT_FILE = "clean_history.jsonl"
OUTPUT_FILE = "cleaned_data.jsonl"

EMOJI_PATTERN = re.compile(r'\[([^\[\]]+)\]')
URL_PATTERN = re.compile(r'https?://[^\s\u3000\uff0c\u3001\u300a\u300b\u201c\u201d]+')
AT_PATTERN = re.compile(r'@([\w\u4e00-\u9fff_-]+)')
RETRACT_PATTERN = re.compile(r'.+撤回了一条消息$')

SYSTEM_KEYWORDS = [
    '加入了群聊', '退出了群聊', '被移出群聊',
    '修改了群名', '群公告', '被设为管理员',
    '已成为新群主', '开启了全员禁言', '关闭了全员禁言'
]

# 需要排除的 sender
EXCLUDED_SENDERS = {'粉丝群'}

# 需要排除的 (sender, 消息关键词) 组合
# 当 sender 匹配且消息包含关键词时，该消息会被丢弃
EXCLUDED_SENDER_MSG_RULES = [
    ('tombkeeper', '加入了群'),
]


def classify_message(message):
    """对消息进行分类"""
    if not message or not message.strip():
        return "empty"
    if RETRACT_PATTERN.match(message):
        return "retracted"
    for kw in SYSTEM_KEYWORDS:
        if kw in message:
            return "system"
    if message.strip() == "分享图片":
        return "image"
    if URL_PATTERN.search(message):
        return "link"
    if message.strip().startswith('@'):
        return "at_mention"
    return "text"


def extract_emojis(msg):
    """提取消息中的表情标记"""
    return EMOJI_PATTERN.findall(msg) if msg else []


def extract_urls(msg):
    """提取消息中的URL"""
    return URL_PATTERN.findall(msg) if msg else []


def extract_mentioned_users(msg):
    """提取消息中@提及的用户"""
    return AT_PATTERN.findall(msg) if msg else []


def clean_text(msg):
    """去掉表情标记和URL后的纯文本"""
    if not msg:
        return ""
    t = EMOJI_PATTERN.sub('', msg)
    t = URL_PATTERN.sub('', t)
    return t.strip()


def should_exclude(sender, msg):
    """判断该消息是否应被排除"""
    # 排除特定 sender
    if sender in EXCLUDED_SENDERS:
        return True
    # 排除 (sender, 关键词) 组合
    for rule_sender, rule_keyword in EXCLUDED_SENDER_MSG_RULES:
        if sender == rule_sender and rule_keyword in msg:
            return True
    return False


def main():
    print("=" * 60)
    print("微博群聊数据清洗")
    print("=" * 60)

    # 读取
    print("[1/4] 读取数据...")
    raw = []
    errs = 0
    with open(INPUT_FILE, 'r', encoding='utf-8') as f:
        for i, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                raw.append(json.loads(line))
            except json.JSONDecodeError:
                errs += 1
    print(f"  {len(raw):,} 条, {errs} 条解析失败")

    # 去重
    print("[2/4] 去重...")
    seen = set()
    deduped = []
    dups = 0
    for r in raw:
        mid = r.get('id')
        if mid in seen:
            dups += 1
            continue
        seen.add(mid)
        deduped.append(r)
    print(f"  去重前 {len(raw):,}, 去重后 {len(deduped):,}, 移除 {dups:,}")

    # 清洗
    print("[3/4] 清洗与字段增强...")
    cleaned = []
    type_counter = Counter()
    sender_counter = Counter()
    emoji_counter = Counter()
    date_counter = Counter()
    hour_counter = Counter()
    time_fmt_errors = 0
    missing_fields = 0
    excluded_count = 0

    for r in deduped:
        if any(f not in r for f in ['id', 'time', 'sender', 'message']):
            missing_fields += 1
            continue
        msg = r.get('message', '') or ''
        sender = r.get('sender', '') or ''
        ts = r.get('time', '')

        # 排除过滤
        if should_exclude(sender, msg):
            excluded_count += 1
            continue

        # 时间解析
        date_str = ''
        hour = -1
        try:
            dt = datetime.strptime(ts, '%Y-%m-%d %H:%M:%S')
            date_str = dt.strftime('%Y-%m-%d')
            hour = dt.hour
        except (ValueError, TypeError):
            time_fmt_errors += 1
            date_str = ts[:10] if len(ts) >= 10 else ''

        # 分类 & 提取
        msg_type = classify_message(msg)
        type_counter[msg_type] += 1
        emojis = extract_emojis(msg)
        urls = extract_urls(msg)
        mentioned = extract_mentioned_users(msg)
        text_clean = clean_text(msg)

        # 统计
        sender_counter[sender] += 1
        for e in emojis:
            emoji_counter[e] += 1
        if date_str:
            date_counter[date_str] += 1
        if hour >= 0:
            hour_counter[hour] += 1

        cleaned.append({
            'id': r['id'],
            'time': ts,
            'date': date_str,
            'hour': hour,
            'sender': sender,
            'message': msg,
            'msg_type': msg_type,
            'text_clean': text_clean,
            'emojis': emojis or None,
            'urls': urls or None,
            'mentioned_users': mentioned or None,
            'downloaded_media': r.get('downloaded_media'),
        })

    print(f"  有效记录: {len(cleaned):,}")
    if excluded_count:
        print(f"  过滤排除: {excluded_count} 条")
    if missing_fields:
        print(f"  缺字段跳过: {missing_fields}")
    if time_fmt_errors:
        print(f"  时间格式异常: {time_fmt_errors}")

    # 排序输出
    print("[4/4] 按时间排序并输出...")
    cleaned.sort(key=lambda x: x['time'])
    with open(OUTPUT_FILE, 'w', encoding='utf-8') as f:
        for c in cleaned:
            f.write(json.dumps(c, ensure_ascii=False) + '\n')
    print(f"  已保存: {OUTPUT_FILE}")

    # ========== 清洗报告 ==========
    n = len(cleaned)
    print("\n" + "=" * 60)
    print("清洗报告")
    print("=" * 60)
    print(f"\n数据规模: 原始 {len(raw):,} -> 去重 {len(deduped):,} -> 有效 {n:,}")

    if date_counter:
        dates = sorted(date_counter.keys())
        print(f"时间跨度: {dates[0]} ~ {dates[-1]}, 共 {len(dates)} 天")

    print(f"\n消息类型分布:")
    for t, c in type_counter.most_common():
        print(f"  {t:15s}: {c:>8,} ({c / n * 100:5.1f}%)")

    print(f"\n发送者: {len(sender_counter):,} 人")
    print("Top 20 活跃用户:")
    for s, c in sender_counter.most_common(20):
        print(f"  {s:20s}: {c:>6,} ({c / n * 100:4.1f}%)")

    if hour_counter:
        mx = max(hour_counter.values())
        print(f"\n每小时消息分布:")
        for h in range(24):
            c = hour_counter.get(h, 0)
            bar = '*' * int(c / mx * 30) if mx else ''
            print(f"  {h:02d}:00 {c:>6,} {bar}")

    if emoji_counter:
        print(f"\n热门表情 Top 15:")
        for e, c in emoji_counter.most_common(15):
            print(f"  [{e}]: {c:,}")

    if date_counter:
        dates = sorted(date_counter.keys())
        mx = max(date_counter.values())
        print(f"\n最近10天消息量:")
        for d in dates[-10:]:
            c = date_counter[d]
            bar = '*' * int(c / mx * 40)
            print(f"  {d}: {c:>5,} {bar}")

    print("\n" + "=" * 60)
    print("完成!")
    print("=" * 60)


if __name__ == '__main__':
    main()
