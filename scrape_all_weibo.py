import json
import logging
import os
import time
import requests

from playwright.sync_api import sync_playwright

logging.basicConfig(level=logging.INFO, format="%(asctime)s - %(message)s")

HISTORY_FILE = "weibo_1401527553_all_history.jsonl"
TARGET_UID = "1401527553"

# 全局持有 headers，供各函数使用
_session_headers = {}

def get_seen_ids():
    seen = set()
    if os.path.exists(HISTORY_FILE):
        with open(HISTORY_FILE, "r", encoding="utf-8") as f:
            for line in f:
                try:
                    data = json.loads(line)
                    seen.add(str(data.get("id")))
                except Exception:
                    pass
    return seen

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
            return filepath

        headers = {
            "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
            "Referer": "https://weibo.com/",
        }
        response = requests.get(url, headers=headers, stream=True, timeout=15)
        response.raise_for_status()

        with open(filepath, "wb") as f:
            for chunk in response.iter_content(chunk_size=8192):
                f.write(chunk)
        return filepath
    except Exception as e:
        logging.error(f"   ❌ 下载失败 {url}: {e}")
        return None

def get_full_text(mblogid):
    """如果微博标记了 isLongText，调用长文接口获取完整正文"""
    try:
        url = f"https://weibo.com/ajax/statuses/longtext?id={mblogid}"
        resp = requests.get(url, headers=_session_headers, timeout=10)
        resp.raise_for_status()
        data = resp.json()
        full_text = data.get("data", {}).get("longTextContent", "")
        if full_text:
            return full_text
    except Exception as e:
        logging.warning(f"   ⚠️ 获取长文失败 ({mblogid}): {e}")
    return None

def extract_text(weibo_data):
    """提取微博完整正文，如果是长文则请求补全"""
    text = weibo_data.get("text_raw", "")
    is_long = weibo_data.get("isLongText", False)
    mblogid = weibo_data.get("mblogid", "")
    if is_long and mblogid:
        full = get_full_text(mblogid)
        if full:
            logging.info(f"   📄 获取到长文全文（mblogid={mblogid}）")
            text = full
            time.sleep(0.5)  # 轻微限速，防止被封
    return text

def extract_media(data):
    if not data:
        return []
    urls = []
    page_info = data.get("page_info", {})
    if page_info and page_info.get("type") == "video":
        media_info = page_info.get("media_info", {})
        video_url = media_info.get("mp4_720p_mp4") or media_info.get("mp4_sd_url") or media_info.get("stream_url")
        if video_url:
            urls.append(video_url)

    pic_infos = data.get("pic_infos", {})
    for pic_id, pic_data in pic_infos.items():
        if "mw2000" in pic_data:
            urls.append(pic_data["mw2000"]["url"])
        elif "original" in pic_data:
            urls.append(pic_data["original"]["url"])
        elif "large" in pic_data:
            urls.append(pic_data["large"]["url"])
            
    if not pic_infos and data.get("pic_ids"):
        for pid in data["pic_ids"]:
            urls.append(f"https://wx1.sinaimg.cn/mw2000/{pid}.jpg")
    return urls

def fetch_all_history(playwright):
    global _session_headers
    
    user_data_dir = "./weibo_user_data"
    logging.info("启动配置提取，获取 Cookie...")

    browser = playwright.chromium.launch_persistent_context(
        user_data_dir, headless=True
    )
    page = browser.pages[0] if browser.pages else browser.new_page()
    page.goto(f"https://weibo.com/u/{TARGET_UID}")
    page.wait_for_timeout(3000)

    cookies = browser.cookies("https://weibo.com")
    cookie_str = "; ".join([f"{c['name']}={c['value']}" for c in cookies])
    browser.close()
    
    logging.info("成功获取浏览器登录态，切换为底层 API 疾速爬取模式。")

    _session_headers = {
        "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
        "Cookie": cookie_str,
        "Referer": f"https://weibo.com/u/{TARGET_UID}",
        "x-requested-with": "XMLHttpRequest",
        "accept": "application/json, text/plain, */*"
    }

    seen_ids = get_seen_ids()
    api_url_base = "https://weibo.com/ajax/statuses/mymblog"

    page_num = 1
    total_new_saved = 0
    all_weibos_in_memory = []
    
    logging.info("============= 第一阶段：极速检索所有微博 =============")
    while page_num < 10:
        try:
            url = f"{api_url_base}?uid={TARGET_UID}&page={page_num}&feature=0"
            res = requests.get(url, headers=_session_headers, timeout=10)
            res.raise_for_status()
            data = res.json()
            
            weibo_list = data.get("data", {}).get("list", [])
            if not weibo_list:
                logging.info(f"⭕️ 在第 {page_num} 页未获取到数据，推测已达历史尽头。")
                break
                
            logging.info(f"正在读取第 {page_num} 页，获取到 {len(weibo_list)} 条记录...")
            
            overlap = False
            for w in weibo_list:
                if str(w.get("id")) in seen_ids:
                    overlap = True
            
            all_weibos_in_memory.extend(weibo_list)
            
            if overlap:
                logging.info(f"⭕️ 在第 {page_num} 页发现了已记录的数据，增量模式下停止追溯。")
                break

            page_num += 1
            time.sleep(2)
            
        except Exception as e:
            logging.error(f"❌ 获取第 {page_num} 页失败: {e}，稍作休息...")
            time.sleep(5)
            continue

    logging.info("\n============= 第二阶段：以最旧 -> 最新 的顺序提取全文、下载图片并入库 =============")
    
    # 去重
    unique_weibos = {}
    target_weibos = []
    for w in all_weibos_in_memory:
        wid = str(w.get("id"))
        if wid not in unique_weibos:
            unique_weibos[wid] = w
            target_weibos.append(w)
            
    with open(HISTORY_FILE, "a", encoding="utf-8") as f:
        for weibo in reversed(target_weibos):
            wid = str(weibo.get("id"))
            if wid in seen_ids:
                continue
            
            created_at = weibo.get("created_at", "")

            # 提取正文（如果是长文则补全）
            final_text = extract_text(weibo)
            media_urls = extract_media(weibo)
            
            is_retweet = False
            if "retweeted_status" in weibo:
                is_retweet = True
                rt = weibo["retweeted_status"]
                if rt:
                    user_info = rt.get("user", {})
                    rt_author = user_info.get("screen_name", "Unknown") if user_info else "Unknown"
                    # 被转发的原文也需要单独检查是否是长文
                    rt_text = extract_text(rt)
                    final_text += f"\n\n[转发自 @{rt_author}]:\n{rt_text}"
                    media_urls.extend(extract_media(rt))
                else:
                    final_text += "\n\n[转发内容已被原作者删除]"
            
            # 下载媒体
            local_media_paths = []
            if media_urls:
                logging.info(f"   ⬇️ 正在为微博 [{wid}] 下载 {len(media_urls)} 个媒体文件...")
            for murl in media_urls:
                local_path = download_media(murl)
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
            
            f.write(json.dumps(record, ensure_ascii=False) + "\n")
            seen_ids.add(wid)
            total_new_saved += 1
            logging.info(f"✅ 入库 -> ID: {wid} | 时间: {created_at}")

    logging.info(f"🎉 全部处理完毕！本次共提取新历史记录 {total_new_saved} 条。")


if __name__ == "__main__":
    with sync_playwright() as playwright:
        fetch_all_history(playwright)
