import json
import logging
import os
import sys
import time
import requests
import re
from urllib.parse import urlparse

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
        return

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
            return

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
            
    except Exception as e:
        logging.error(f"解析内容失败，有可能接口未能返回期望的JSON。错误信息: {e}")
        
    browser.close()

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("用法: python scrape_weibo_link.py <weibo_link>")
        sys.exit(1)
        
    target_url = sys.argv[1]
    with sync_playwright() as playwright:
        run_scraper(playwright, target_url)
