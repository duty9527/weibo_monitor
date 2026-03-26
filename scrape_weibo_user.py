import json
import logging
import os
import time
import requests

from playwright.sync_api import sync_playwright

logging.basicConfig(level=logging.INFO, format="%(asctime)s - %(message)s")

HISTORY_FILE = "weibo_1401527553_history.jsonl"
TARGET_UID = "1401527553"
TARGET_URL = f"https://www.weibo.com/u/{TARGET_UID}"

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
            return filepath # 跳过已经存在的下载

        headers = {
            "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
            "Referer": "https://weibo.com/",
        }
        logging.info(f"   ⬇️ 正在下载: {url} -> {filepath}")
        response = requests.get(url, headers=headers, stream=True, timeout=15)
        response.raise_for_status()

        with open(filepath, "wb") as f:
            for chunk in response.iter_content(chunk_size=8192):
                f.write(chunk)
        return filepath
    except Exception as e:
        logging.error(f"   ❌ 下载失败 {url}: {e}")
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

def run_scraper(playwright):
    user_data_dir = "./weibo_user_data"
    logging.info(f"启动爬虫任务，使用的浏览器配置路径: {user_data_dir}")

    browser = playwright.chromium.launch_persistent_context(
        user_data_dir, headless=False, viewport={"width": 1280, "height": 800}
    )

    page = browser.pages[0] if browser.pages else browser.new_page()
    
    seen_ids = get_seen_ids()
    new_weibos = []

    def handle_response(response):
        if "ajax/statuses/mymblog" in response.url and response.status == 200:
            try:
                data = response.json()
                if data and "data" in data and "list" in data["data"]:
                    weibo_list = data["data"]["list"]
                    logging.info(f"拦截到 API 请求并获取到最新 {len(weibo_list)} 条数据。")
                    new_weibos.extend(weibo_list)
            except Exception as e:
                logging.error(f"解析 API 失败: {e}")

    page.on("response", handle_response)
    
    logging.info(f"正在前往用户的微博主页: {TARGET_URL}")
    page.goto(TARGET_URL)
    
    page.wait_for_timeout(3000)
    
    # 动态滚动加载直到遇到看到过的历史为止，突破 20 条限制
    max_scrolls = 30 # 做一个安全跳出上限，一次请求20条，30次滚动等于抓取近 600 条
    scroll_count = 0
    
    while scroll_count < max_scrolls:
        # 判断刚拿到的数据里，是不是已经出现过 seen_ids 里面记录的旧数据
        found_seen = False
        for w in new_weibos[-20:]: # 检查每批新数据
            target_id = str(w.get("id"))
            # 忽略置顶微博造成的错误跳出，置顶微博有个 isTop 标记（但这里粗略处理：如果存在旧数据，且新数据有大于等于2个老数据，大概率说明接上了。我们简单以见到旧数据就停止为准）
            if target_id in seen_ids:
                found_seen = True
                break
                
        if found_seen:
            logging.info("♻️ 发现已抓取过的微博记录，停止滚动并结束向下挖掘。")
            break
            
        logging.info("🔽 模拟下拉加载更多历史数据...")
        # 模拟键盘按到底部
        page.keyboard.press("End")
        page.wait_for_timeout(2000)
        scroll_count += 1
    
    if not new_weibos:
        logging.warning("未能通过网络请求拿到数据，可能是当前 API 不是此路径，或者没有新的请求产生。")
    else:
        added_count = 0
        unique_weibos = {}
        target_weibos = []
        for w in new_weibos:
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
                text_raw = weibo.get("text_raw", "")
                
                final_text = text_raw
                media_urls = extract_media(weibo)
                
                is_retweet = False
                if "retweeted_status" in weibo:
                    is_retweet = True
                    rt = weibo["retweeted_status"]
                    rt_author = rt.get("user", {}).get("screen_name", "Unknown")
                    rt_text = rt.get("text_raw", "")
                    
                    final_text += f"\n\n[转发自 @{rt_author}]:\n{rt_text}"
                    media_urls.extend(extract_media(rt))
                
                # 开始下载所有媒体图片并记录存放位置
                local_media_paths = []
                for url in media_urls:
                    local_path = download_media(url)
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
                added_count += 1
                
                logging.info(f"✨ 新增处理 -> 微博 ID: {wid} | 时间: {created_at}")

        if added_count == 0:
            logging.info("所有获取到的微博都是已记录的旧数据。情况已同步至最新。")
        else:
            logging.info(f"✅ 获取操作完成，成功保存了 {added_count} 条新微博并下载图片。")
            
    browser.close()

if __name__ == "__main__":
    with sync_playwright() as playwright:
        run_scraper(playwright)
