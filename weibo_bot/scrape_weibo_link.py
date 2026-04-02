import json
import logging
import os
import sys
import time
import requests
import re
from urllib.parse import urlparse
from datetime import datetime, timezone

from playwright.sync_api import sync_playwright

logging.basicConfig(level=logging.INFO, format="%(asctime)s - %(message)s")

def extract_weibo_id(url):
    """从不同格式的微博URL中提取微博ID"""
    parsed = urlparse(url)
    path = parsed.path
    
    # 移除末尾的斜杠
    if path.endswith('/'):
        path = path[:-1]
        
    parts = path.split('/')
    
    # 比如 /1401527553/NB4vXy3aP -> id 是 NB4vXy3aP
    # 比如 /detail/4962291583582458 -> id 是 4962291583582458
    # 比如 /status/NB4vXy3aP -> id 是 NB4vXy3aP
    if len(parts) >= 2:
        # 通常最后一部分就是 id
        return parts[-1]
    return None

def download_media(url, folder="media_downloads"):
    if not url:
        return None
    try:
        os.makedirs(folder, exist_ok=True)
        filename = url.split("/")[-1].split("?")[0]
        if not filename:
            filename = f"media_{int(time.time())}.file"

        filepath = os.path.join(folder, filename)
        if os.path.exists(filepath):
            return filepath # 跳过已经存在的下载

        headers = {
            "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
            "Referer": "https://weibo.com/",
        }
        logging.info(f"   ⬇️ 正在下载媒体: {url} -> {filepath}")
        response = requests.get(url, headers=headers, stream=True, timeout=15)
        response.raise_for_status()

        with open(filepath, "wb") as f:
            for chunk in response.iter_content(chunk_size=8192):
                f.write(chunk)
        return filepath
    except Exception as e:
        logging.error(f"   ❌ 下载媒体失败 {url}: {e}")
        return None

def extract_media(data):
    urls = []
    
    # 提取视频
    page_info = data.get("page_info", {})
    if page_info and page_info.get("type") == "video":
        media_info = page_info.get("media_info", {})
        video_url = media_info.get("mp4_720p_mp4") or media_info.get("mp4_sd_url") or media_info.get("stream_url")
        if video_url:
            urls.append(video_url)

    # 提取图片
    pic_infos = data.get("pic_infos", {})
    for pic_id, pic_data in pic_infos.items():
        if "mw2000" in pic_data:
            urls.append(pic_data["mw2000"]["url"])
        elif "original" in pic_data:
            urls.append(pic_data["original"]["url"])
        elif "large" in pic_data:
            urls.append(pic_data["large"]["url"])
            
    # 一些并没有 pic_infos 的图库规则兜底
    if not pic_infos and data.get("pic_ids"):
        for pid in data["pic_ids"]:
            urls.append(f"https://wx1.sinaimg.cn/mw2000/{pid}.jpg")

    return urls

def run_scraper(playwright, url):
    user_data_dir = "./weibo_user_data"
    logging.info(f"启动单链接爬虫任务，使用的浏览器配置路径: {user_data_dir}")

    browser = playwright.chromium.launch_persistent_context(
        user_data_dir, headless=True, viewport={"width": 1280, "height": 800}
    )

    page = browser.pages[0] if browser.pages else browser.new_page()
    
    weibo_id = extract_weibo_id(url)
    if not weibo_id:
        logging.error(f"无法从链接 {url} 提取出微博 ID")
        browser.close()
        return None

    # 直接使用 API 请求，更为直接和高效
    # 构造接口 URL: https://weibo.com/ajax/statuses/show?id={weibo_id}
    api_url = f"https://weibo.com/ajax/statuses/show?id={weibo_id}"
    logging.info(f"直接前往微博内容 API: {api_url}")
    
    # 先导航到首页获取Cookie，防 403
    page.goto("https://weibo.com/")
    page.wait_for_timeout(2000)
    cookies = browser.cookies("https://weibo.com")
    cookie_str = "; ".join([f"{c['name']}={c['value']}" for c in cookies])
    
    session_headers = {
        "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
        "Cookie": cookie_str,
        "Referer": "https://weibo.com/",
        "x-requested-with": "XMLHttpRequest",
        "accept": "application/json, text/plain, */*"
    }

    try:
        res = requests.get(api_url, headers=session_headers, timeout=10)
        res.raise_for_status()
        weibo = res.json()

        if "error_type" in weibo or "error_code" in weibo:
            logging.error(f"API 返回错误: {weibo.get('error_type') or weibo.get('message')}")
            browser.close()
            return None

        wid = str(weibo.get("id"))
        created_at = weibo.get("created_at", "")
        text_raw = weibo.get("text_raw", "")
        
        final_text = text_raw
        media_urls = extract_media(weibo)
        
        is_retweet = False
        rt_author = ""
        rt_text = ""
        if "retweeted_status" in weibo:
            is_retweet = True
            rt = weibo["retweeted_status"]
            rt_author = rt.get("user", {}).get("screen_name", "Unknown")
            rt_text = rt.get("text_raw", "")
            
            if rt.get("isLongText"):
                try:
                    rt_id = str(rt.get("mblogid") or rt.get("id"))
                    logging.info("发现该转发微博是长文本，尝试获取转发的完整长文本...")
                    rt_long_api = f"https://weibo.com/ajax/statuses/longtext?id={rt_id}"
                    rt_long_res = requests.get(rt_long_api, headers=session_headers, timeout=10)
                    rt_long_data = rt_long_res.json()
                    if "data" in rt_long_data and "longTextContent" in rt_long_data["data"]:
                        rt_text = rt_long_data["data"]["longTextContent"]
                except Exception as e:
                    logging.warning(f"获取转发长文本失败: {e}")
            
            final_text += f"\n\n[转发自 @{rt_author}]:\n{rt_text}"
            media_urls.extend(extract_media(rt))
        
        # 尝试检查主体是否是长文本
        if weibo.get("isLongText"):
            try:
                logging.info("发现主体微博是长文本，尝试获取完整长文本...")
                long_text_api = f"https://weibo.com/ajax/statuses/longtext?id={weibo_id}"
                long_res = requests.get(long_text_api, headers=session_headers, timeout=10)
                long_data = long_res.json()
                if "data" in long_data and "longTextContent" in long_data["data"]:
                    final_text = long_data["data"]["longTextContent"]
                    # 重新拼接转发结构
                    if is_retweet:
                        final_text += f"\n\n[转发自 @{rt_author}]:\n{rt_text}"
            except Exception as e:
                logging.warning(f"获取长文本失败: {e}")

        # 开始下载所有媒体图片并记录存放位置
        local_media_paths = []
        for m_url in media_urls:
            local_path = download_media(m_url)
            if local_path:
                local_media_paths.append(local_path)
        
        record = {
            "id": wid,
            "created_at": created_at,
            "text": final_text,
            "media_urls": media_urls,
            "local_media_paths": local_media_paths,
            "is_retweet": is_retweet
        }
        
        # 打印到控制台
        print("\n" + "="*50)
        print("已抓取微博:")
        print(f"ID: {record['id']}")
        print(f"创建时间: {record['created_at']}")
        print(f"是否转发: {record['is_retweet']}")
        print("-" * 20 + " 正文 " + "-" * 20)
        print(record['text'])
        print("-" * 20 + " 媒体 " + "-" * 20)
        for p in record['local_media_paths']:
            print(f"本地文件: {p}")
        print("="*50 + "\n")
        
        # 保存到独立文件
        save_file = f"weibo_{wid}.json"
        with open(save_file, "w", encoding="utf-8") as f:
            json.dump(record, f, ensure_ascii=False, indent=4)
        logging.info(f"✅ 抓取已完成，独立保存至 {save_file}")
        return record
            
    except Exception as e:
        logging.error(f"解析内容失败，有可能接口未能返回期望的JSON。错误信息: {e}")
        
    browser.close()
    return None

# ─────────────────────────── Telegram 推送 ───────────────────────────

TEXT_LIMIT    = 4096
CAPTION_LIMIT = 1024
MEDIA_GROUP_LIMIT = 10

PHOTO_EXTS  = {".jpg", ".jpeg", ".png", ".webp"}
VIDEO_EXTS  = {".mp4", ".mov", ".m4v"}


def _detect_media_kind(path):
    """识别媒体类型，返回 (type, field)，对应 Go 的 detectMediaKind"""
    ext = os.path.splitext(path)[1].lower()
    if ext in PHOTO_EXTS:
        return "photo", "photo"
    if ext in VIDEO_EXTS:
        return "video", "video"
    return "document", "document"


def _all_groupable(items):
    """检查所有媒体项是否都可以放入 mediaGroup（photo/video）"""
    return all(t in ("photo", "video") for t, _ in items)


def _format_display_time(created_at):
    """格式化时间，对应 Go 的 formatDisplayTime"""
    if not created_at:
        return ""
    # 微博 API 时间格式示例: "Tue Mar 25 10:30:00 +0800 2025"
    for fmt in ("%a %b %d %H:%M:%S %z %Y", "%Y-%m-%dT%H:%M:%S%z"):
        try:
            dt = datetime.strptime(created_at, fmt)
            # 转为本地显示
            local_dt = dt.astimezone()
            return "【" + local_dt.strftime("%y-%m-%d %H:%M:%S") + "】"
        except ValueError:
            continue
    return "【" + created_at + "】"


def _format_record_message(record, source_url=""):
    """格式化推送正文，对应 Go 的 formatRecordMessage"""
    lines = []
    tag = "#转发" if record.get("is_retweet") else "#原创"
    time_str = _format_display_time(record.get("created_at", ""))
    header = (tag + " " + time_str).strip()
    if header:
        lines.append(header)

    text = (record.get("text") or "").strip()
    if text:
        lines.append("")
        lines.append(text)

    if source_url:
        lines.append("")
        lines.append(source_url)

    if not lines:
        return "微博更新"
    return "\n".join(lines).strip()


def _split_text(text, limit):
    """按字符数分割文本，对应 Go 的 splitText"""
    if not text:
        return []
    runes = list(text)
    if len(runes) <= limit:
        return [text]
    chunks = []
    while len(runes) > limit:
        cut = limit
        for i in range(limit, limit // 2, -1):
            if runes[i - 1] == "\n":
                cut = i
                break
        chunks.append("".join(runes[:cut]).strip())
        runes = runes[cut:]
    if runes:
        chunks.append("".join(runes).strip())
    return chunks


def _tg_api_url(bot_token, method):
    return f"https://api.telegram.org/bot{bot_token}/{method}"


def _check_tg_response(resp, method):
    """读取响应体后再判断错误，确保错误信息包含 Telegram 的真实描述"""
    body = resp.text  # 先读取，避免 raise_for_status 把响应体丢掉
    if not resp.ok:
        try:
            desc = resp.json().get("description", body)
        except Exception:
            desc = body
        raise RuntimeError(f"{method} 失败 (HTTP {resp.status_code}): {desc}")
    try:
        result = resp.json()
        if not result.get("ok"):
            raise RuntimeError(f"{method} 失败: {result.get('description')}")
    except RuntimeError:
        raise
    except Exception:
        pass


def _tg_send_text(bot_token, chat_id, thread_id, text, enable_preview=False):
    """发送纯文本消息（自动分块），对应 Go 的 sendText"""
    text = (text or "").strip()
    if not text:
        return
    for chunk in _split_text(text, TEXT_LIMIT):
        payload = {"chat_id": chat_id, "text": chunk}
        if thread_id:
            payload["message_thread_id"] = thread_id
        if not enable_preview:
            payload["disable_web_page_preview"] = True
        resp = requests.post(_tg_api_url(bot_token, "sendMessage"), json=payload, timeout=30)
        _check_tg_response(resp, "sendMessage")


def _tg_send_single_media(bot_token, chat_id, thread_id, path, media_type, caption=""):
    """发送单个媒体文件，对应 Go 的 sendSingleMedia。
    若 sendPhoto/sendVideo 因尺寸问题被拒绝，自动降级为 sendDocument。"""
    method_map = {"photo": "sendPhoto", "video": "sendVideo", "document": "sendDocument"}
    method = method_map.get(media_type, "sendDocument")
    data = {"chat_id": chat_id}
    if thread_id:
        data["message_thread_id"] = str(thread_id)
    caption = (caption or "").strip()
    if caption:
        data["caption"] = caption

    with open(path, "rb") as f:
        files = {media_type: (os.path.basename(path), f)}
        resp = requests.post(_tg_api_url(bot_token, method), data=data, files=files, timeout=60)

    # 若图片/视频尺寸不合法，自动降级为文件发送
    if not resp.ok:
        try:
            tg_desc = resp.json().get("description", "")
        except Exception:
            tg_desc = ""
        if any(kw in tg_desc for kw in ("PHOTO_INVALID_DIMENSIONS", "PHOTO_SAVE_FILE_INVALID",
                                         "VIDEO_FILE_INVALID", "failed to get HTTP URL content")):
            logging.warning(f"⚠️  {method} 被拒绝（{tg_desc}），降级为 sendDocument 重试: {os.path.basename(path)}")
            data2 = {k: v for k, v in data.items()}  # 复制，避免修改原 dict
            with open(path, "rb") as f:
                files2 = {"document": (os.path.basename(path), f)}
                resp = requests.post(_tg_api_url(bot_token, "sendDocument"), data=data2, files=files2, timeout=60)
            _check_tg_response(resp, "sendDocument")
            return

    _check_tg_response(resp, method)


def _tg_send_media_group(bot_token, chat_id, thread_id, items_with_paths, caption=""):
    """发送媒体组，对应 Go 的 sendMediaGroup"""
    data = {"chat_id": chat_id}
    if thread_id:
        data["message_thread_id"] = str(thread_id)
    caption = (caption or "").strip()

    media_json = []
    files = {}
    for i, (path, media_type) in enumerate(items_with_paths):
        attach_name = f"file{i}"
        entry = {
            "type": media_type,
            "media": f"attach://{attach_name}",
        }
        if i == 0 and caption:
            entry["caption"] = caption
        media_json.append(entry)
        files[attach_name] = (os.path.basename(path), open(path, "rb"))

    data["media"] = json.dumps(media_json, ensure_ascii=False)
    try:
        resp = requests.post(_tg_api_url(bot_token, "sendMediaGroup"), data=data, files=files, timeout=60)
    finally:
        for f in files.values():
            f[1].close()
    _check_tg_response(resp, "sendMediaGroup")


def _tg_send_media_set(bot_token, chat_id, thread_id, items_with_paths, caption=""):
    """发送媒体集合，对应 Go 的 sendMediaSet"""
    if not items_with_paths:
        return
    if len(items_with_paths) == 1:
        path, media_type = items_with_paths[0]
        _tg_send_single_media(bot_token, chat_id, thread_id, path, media_type, caption)
        return

    if _all_groupable([(t, p) for p, t in items_with_paths]):
        caption_used = False
        for start in range(0, len(items_with_paths), MEDIA_GROUP_LIMIT):
            chunk = items_with_paths[start:start + MEDIA_GROUP_LIMIT]
            group_caption = caption if not caption_used else ""
            caption_used = True
            _tg_send_media_group(bot_token, chat_id, thread_id, chunk, group_caption)
        return

    # 含非 photo/video 媒体，逐个发送
    caption_used = False
    for path, media_type in items_with_paths:
        item_caption = caption if not caption_used else ""
        caption_used = True
        _tg_send_single_media(bot_token, chat_id, thread_id, path, media_type, item_caption)


def send_record_to_telegram(record, bot_token, chat_id, thread_id=None, source_url=""):
    """
    将一条抓取结果推送到 Telegram，对应 Go 端的 SendRecord 函数。

    参数:
        record       - run_scraper 返回的 dict
        bot_token    - Telegram Bot Token
        chat_id      - 目标 chat_id（字符串，支持频道/群组负数 ID）
        thread_id    - 话题 ID（可选，对应 message_thread_id）
        source_url   - 微博原文链接（可选，会拼接在消息末尾）
    """
    body = _format_record_message(record, source_url)

    local_paths = [p for p in (record.get("local_media_paths") or []) if p and os.path.isfile(p)]
    media_urls  = record.get("media_urls") or []
    # 找出下载失败的原始链接（local_media_paths 比 media_urls 少的部分即失败项）
    failed_urls = media_urls[len(local_paths):] if len(media_urls) > len(local_paths) else []

    items = [(p, _detect_media_kind(p)[0]) for p in local_paths]

    if not items:
        # 无媒体 → 纯文本，若有原始链接则开启预览
        enable_preview = bool(source_url)
        _tg_send_text(bot_token, chat_id, thread_id, body, enable_preview)
        if failed_urls:
            fallback = "以下媒体未成功下载，原始链接如下：\n" + "\n".join(failed_urls)
            _tg_send_text(bot_token, chat_id, thread_id, fallback, False)
        return

    # 有媒体
    can_use_caption = len(body) <= CAPTION_LIMIT
    if can_use_caption:
        _tg_send_media_set(bot_token, chat_id, thread_id, items, body)
    else:
        logging.info("正文超过 Telegram caption 限制，拆分为文本消息 + 媒体消息")
        _tg_send_text(bot_token, chat_id, thread_id, body, False)
        _tg_send_media_set(bot_token, chat_id, thread_id, items, "")

    if failed_urls:
        fallback = "部分媒体未成功下载，原始链接如下：\n" + "\n".join(failed_urls)
        _tg_send_text(bot_token, chat_id, thread_id, fallback, False)


# ────────────────────────────── 入口 ──────────────────────────────

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("用法: python scrape_weibo_link.py <weibo_link> [bot_token] [chat_id] [thread_id]")
        print("      或通过环境变量 TG_BOT_TOKEN / TG_CHAT_ID / TG_THREAD_ID 配置")
        sys.exit(1)

    target_url = sys.argv[1]

    # Telegram 配置：优先取命令行参数，其次取环境变量
    tg_bot_token = (sys.argv[2] if len(sys.argv) > 2 else None) or os.environ.get("TG_BOT_TOKEN", "")
    tg_chat_id   = (sys.argv[3] if len(sys.argv) > 3 else None) or os.environ.get("TG_CHAT_ID", "")
    raw_thread   = (sys.argv[4] if len(sys.argv) > 4 else None) or os.environ.get("TG_THREAD_ID", "")
    tg_thread_id = int(raw_thread) if raw_thread and raw_thread.isdigit() else None

    record = None
    with sync_playwright() as playwright:
        record = run_scraper(playwright, target_url)

    # 如果配置了 Telegram，则推送
    if record and tg_bot_token and tg_chat_id:
        logging.info("开始推送到 Telegram...")
        try:
            send_record_to_telegram(
                record,
                bot_token=tg_bot_token,
                chat_id=tg_chat_id,
                thread_id=tg_thread_id,
                source_url=target_url,
            )
            logging.info("✅ 已成功推送到 Telegram")
        except Exception as e:
            logging.error(f"❌ 推送到 Telegram 失败: {e}")
    elif record and not tg_bot_token:
        logging.info("未配置 Telegram（TG_BOT_TOKEN 为空），跳过推送")
