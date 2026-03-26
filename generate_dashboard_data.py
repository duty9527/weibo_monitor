#!/usr/bin/env python3
"""
从 cleaned_data.jsonl 生成 Dashboard 所需的聚合数据 (dashboard_data.json)
"""

import json
import re
from collections import Counter, defaultdict
from datetime import datetime, timedelta

INPUT_FILE = "cleaned_data.jsonl"
OUTPUT_FILE = "dashboard_data.json"

# 停用词 (常见无意义词)
STOP_WORDS = set("""
的 了 是 在 我 有 和 就 不 人 都 一 一个 上 也 很 到 说 要 去 你 会 着
可以 这 那 他 她 对 但 没 吧 啊 呢 嗯 哈 嘛 么 哦 哈哈 哈哈哈
还 好 不是 没有 什么 这个 那个 自己 已经 感觉 知道 觉得 怎么
大家 还是 可能 真的 确实 其实 应该 现在 所以 因为 如果 就是
而且 但是 不过 然后 或者 虽然 这样 那样 之后 出来 起来 一下
分享图片 撤回了一条消息
""".split())

# 中文标点和特殊字符
PUNCT_PATTERN = re.compile(r'[，。！？、；：""''（）【】《》\s\d\w]+')


def simple_tokenize(text):
    """简单中文分词 (按标点分割后提取2-4字词组)"""
    if not text or len(text) < 2:
        return []
    # 去掉标点分割成片段
    segments = PUNCT_PATTERN.split(text)
    words = []
    for seg in segments:
        seg = seg.strip()
        if len(seg) < 2:
            continue
        if seg in STOP_WORDS:
            continue
        # 对于短片段直接作为词
        if 2 <= len(seg) <= 6:
            words.append(seg)
        else:
            # 长片段提取 2-4 gram
            for n in [2, 3, 4]:
                for i in range(len(seg) - n + 1):
                    w = seg[i:i+n]
                    if w not in STOP_WORDS:
                        words.append(w)
    return words


def try_jieba_tokenize(text):
    """尝试用 jieba 分词"""
    try:
        import jieba
        words = jieba.lcut(text)
        return [w for w in words if len(w) >= 2 and w not in STOP_WORDS]
    except ImportError:
        return None


def main():
    print("加载数据...")
    records = []
    with open(INPUT_FILE, 'r', encoding='utf-8') as f:
        for line in f:
            line = line.strip()
            if line:
                records.append(json.loads(line))
    print(f"  加载 {len(records):,} 条记录")

    # 检测是否有 jieba
    use_jieba = False
    try:
        import jieba
        use_jieba = True
        print("  使用 jieba 分词")
    except ImportError:
        print("  jieba 不可用, 使用简单分词 (pip install jieba 可获得更好效果)")

    # ========== 基础统计 ==========
    print("计算基础统计...")
    total = len(records)
    senders = Counter()
    msg_types = Counter()
    emoji_counter = Counter()
    daily_msgs = Counter()
    hourly_msgs = Counter()
    weekly_msgs = Counter()  # 周几
    monthly_msgs = Counter()

    # 用户每日活跃
    user_daily = defaultdict(set)
    # 每日活跃用户数
    daily_active_users = Counter()

    word_counter = Counter()

    # 热门讨论检测 - 每小时消息数
    hourly_slots = Counter()  # key: "2025-04-07 14"

    for r in records:
        sender = r['sender']
        senders[sender] += 1
        msg_types[r['msg_type']] += 1
        date_str = r['date']
        hour = r['hour']
        daily_msgs[date_str] += 1

        if hour >= 0:
            hourly_msgs[hour] += 1

        # 周几 (0=Monday)
        try:
            dt = datetime.strptime(date_str, '%Y-%m-%d')
            weekly_msgs[dt.weekday()] += 1
            monthly_msgs[dt.strftime('%Y-%m')] += 1
            hourly_slots[f"{date_str} {hour:02d}"] += 1
        except:
            pass

        user_daily[date_str].add(sender)

        # 表情
        if r.get('emojis'):
            for e in r['emojis']:
                emoji_counter[e] += 1

        # 分词 (只处理文本消息)
        if r['msg_type'] == 'text' and r.get('text_clean'):
            text = r['text_clean']
            if use_jieba:
                words = try_jieba_tokenize(text)
                if words:
                    for w in words:
                        word_counter[w] += 1
            else:
                for w in simple_tokenize(text):
                    word_counter[w] += 1

    # 每日活跃用户数
    for date_str, users in user_daily.items():
        daily_active_users[date_str] = len(users)

    # ========== 热门讨论检测 ==========
    print("检测热门讨论...")
    # 找每小时消息量的均值和标准差
    slot_counts = list(hourly_slots.values())
    if slot_counts:
        avg_slot = sum(slot_counts) / len(slot_counts)
        std_slot = (sum((x - avg_slot) ** 2 for x in slot_counts) / len(slot_counts)) ** 0.5
        threshold = avg_slot + 2 * std_slot  # 超过2个标准差视为热门
    else:
        threshold = 999999

    hot_discussions = []
    for slot, count in sorted(hourly_slots.items(), key=lambda x: -x[1])[:50]:
        if count < threshold:
            continue
        date_part = slot[:10]
        hour_part = int(slot[11:13])
        # 获取该时段的消息样本
        samples = []
        for r in records:
            if r['date'] == date_part and r['hour'] == hour_part and r['msg_type'] == 'text':
                samples.append({
                    'time': r['time'],
                    'sender': r['sender'],
                    'message': r['message'][:100]
                })
                if len(samples) >= 5:
                    break
        hot_discussions.append({
            'date': date_part,
            'hour': hour_part,
            'count': count,
            'samples': samples
        })

    hot_discussions = hot_discussions[:30]  # 最多30条

    # ========== 构造输出 ==========
    print("生成输出数据...")
    dates_sorted = sorted(daily_msgs.keys())

    dashboard_data = {
        'overview': {
            'total_messages': total,
            'total_users': len(senders),
            'date_start': dates_sorted[0] if dates_sorted else '',
            'date_end': dates_sorted[-1] if dates_sorted else '',
            'total_days': len(dates_sorted),
            'avg_daily_msgs': round(total / len(dates_sorted)) if dates_sorted else 0,
        },
        'daily_messages': [
            {'date': d, 'count': daily_msgs[d], 'active_users': daily_active_users.get(d, 0)}
            for d in dates_sorted
        ],
        'monthly_messages': [
            {'month': m, 'count': c}
            for m, c in sorted(monthly_msgs.items())
        ],
        'hourly_distribution': [
            {'hour': h, 'count': hourly_msgs.get(h, 0)}
            for h in range(24)
        ],
        'weekly_distribution': [
            {'day': d, 'name': ['周一','周二','周三','周四','周五','周六','周日'][d],
             'count': weekly_msgs.get(d, 0)}
            for d in range(7)
        ],
        'msg_type_distribution': [
            {'type': t, 'count': c}
            for t, c in msg_types.most_common()
        ],
        'top_users': [
            {'name': s, 'count': c, 'pct': round(c / total * 100, 2)}
            for s, c in senders.most_common(50)
        ],
        'top_emojis': [
            {'emoji': e, 'count': c}
            for e, c in emoji_counter.most_common(30)
        ],
        'word_cloud': [
            {'word': w, 'count': c}
            for w, c in word_counter.most_common(200)
        ],
        'hot_discussions': hot_discussions,
    }

    with open(OUTPUT_FILE, 'w', encoding='utf-8') as f:
        json.dump(dashboard_data, f, ensure_ascii=False, indent=2)

    print(f"已保存: {OUTPUT_FILE}")
    print(f"  词云词数: {len(dashboard_data['word_cloud'])}")
    print(f"  热门讨论: {len(hot_discussions)} 条")
    print("完成!")


if __name__ == '__main__':
    main()
