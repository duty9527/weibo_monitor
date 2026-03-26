import json
import logging
import os
import time

import requests
from playwright.sync_api import sync_playwright

# --- 停止条件配置 ---
STOP_CONDITION = {
    "enabled": True,  # 修改为 True 即可开启停止条件判断
    "target_time": "2026-03-10 15:59:48",  # 设置你要停止的时间(包含该内容即可)，留空表示不限制。如 "2023-11-01"
    "target_sender": "germer_123",  # 设置你要停止的发送者或者昵称，留空表示不限制。如 "张三"
    "target_message": "开箱即用模型[doge]",  # 设置你要停止的消息内容，留空表示不限制。如 "测试结束"
}
# --------------------

logging.basicConfig(level=logging.INFO, format="%(asctime)s - %(message)s")


def download_media(url, folder="downloads", filename=None):
    if not url:
        return None
    try:
        os.makedirs(folder, exist_ok=True)
        if not filename:
            filename = url.split("/")[-1].split("?")[0]  # Basic filename extraction
            if not filename:
                filename = f"media_{int(time.time())}.file"

        filepath = os.path.join(folder, filename)

        # Don't download twice
        if os.path.exists(filepath):
            return filepath

        headers = {
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
            "Referer": "https://weibo.com/",
        }
        logging.info(f"Downloading media: {url}...")
        response = requests.get(url, headers=headers, stream=True, timeout=15)
        response.raise_for_status()

        with open(filepath, "wb") as f:
            for chunk in response.iter_content(chunk_size=8192):
                f.write(chunk)
        return filepath
    except Exception as e:
        logging.error(f"Failed to download {url}: {e}")
        return None


def run_app(playwright):
    user_data_dir = "./weibo_user_data"
    logging.info(f"Launching Chromium with user data dir: {user_data_dir}")

    browser = playwright.chromium.launch_persistent_context(
        user_data_dir, headless=False, viewport={"width": 1280, "height": 800}
    )

    page = browser.pages[0] if browser.pages else browser.new_page()
    url = "https://api.weibo.com/chat/#/chat?check_gid=4761715839862414&source_from=11"
    api_url_base = "https://api.weibo.com/webim/groupchat/query_messages.json"

    # Store clean history
    output_file = "clean_history.jsonl"
    media_folder = "media_downloads"

    def process_messages(messages):
        if not messages:
            return None

        # Sort chronologically (oldest first)
        sorted_msgs = sorted(messages, key=lambda x: int(x["id"]))
        should_stop = False

        with open(output_file, "a", encoding="utf-8") as f:
            for msg in sorted_msgs:
                sender_name = msg.get("from_user", {}).get("screen_name", "Unknown")
                text = msg.get("text", "") or msg.get("content", "")
                timestamp = msg.get("time", int(time.time()))
                readable_time = time.strftime(
                    "%Y-%m-%d %H:%M:%S", time.localtime(timestamp)
                )
                mid = msg.get("id")

                # Check stop condition
                if STOP_CONDITION.get("enabled"):
                    time_ok = (
                        (STOP_CONDITION["target_time"] in readable_time)
                        if STOP_CONDITION["target_time"]
                        else True
                    )
                    sender_ok = (
                        (STOP_CONDITION["target_sender"] in sender_name)
                        if STOP_CONDITION["target_sender"]
                        else True
                    )
                    msg_ok = (
                        (STOP_CONDITION["target_message"] in text)
                        if STOP_CONDITION["target_message"]
                        else True
                    )
                    if time_ok and sender_ok and msg_ok:
                        if (
                            STOP_CONDITION["target_time"]
                            or STOP_CONDITION["target_sender"]
                            or STOP_CONDITION["target_message"]
                        ):
                            logging.info(
                                f"🛑 触发停止抓取节点: 时间[{readable_time}] 昵称[{sender_name}] 内容[{text[:30]}]"
                            )
                            should_stop = True

                # Check for media
                media_type = msg.get("media_type", 0)
                media_path = None

                # Weibo handles media (images/videos) differently. Often inside 'page_info' or 'url_struct'
                # But typically direct images in chat sometimes have direct 'fid' or url.
                # Let's aggressively search for any pic URL in the json string or page_info
                page_info = msg.get("page_info", {})
                if page_info and page_info.get("type") in ["pic", "video"]:
                    if page_info.get("type") == "pic":
                        pic_info = page_info.get("page_pic", {})
                        media_url = (
                            pic_info.get("url")
                            or pic_info.get("bmiddle", {}).get("url")
                            or pic_info.get("original", {}).get("url")
                        )
                        if media_url:
                            # ensure secure url
                            if media_url.startswith("//"):
                                media_url = "https:" + media_url
                            elif media_url.startswith("http:"):
                                media_url = media_url.replace("http:", "https:")

                            filename = f"{mid}_image.jpg"
                            media_path = download_media(
                                media_url, media_folder, filename
                            )

                    elif page_info.get("type") == "video":
                        media_info = page_info.get("media_info", {})
                        media_url = (
                            media_info.get("mp4_720p_mp4")
                            or media_info.get("mp4_sd_url")
                            or media_info.get("stream_url")
                        )
                        if media_url:
                            filename = f"{mid}_video.mp4"
                            media_path = download_media(
                                media_url, media_folder, filename
                            )

                # Custom text formatting to keep it clean
                clean_data = {
                    "id": mid,
                    "time": readable_time,
                    "sender": sender_name,
                    "message": text,
                    "downloaded_media": media_path,
                }

                f.write(json.dumps(clean_data, ensure_ascii=False) + "\n")

        # Print progress
        if sorted_msgs:
            newest = sorted_msgs[-1]
            sender_name = newest.get("from_user", {}).get("screen_name", "Unknown")
            text = newest.get("text", "") or newest.get("content", "")
            logging.info(
                f"Processed batch. Latest: [{readable_time}] {sender_name}: {text[:30]}..."
            )
        if should_stop:
            return None

        oldest_msg = min(messages, key=lambda x: int(x["id"]))
        return oldest_msg["id"]

    logging.info(f"Navigating to {url}")

    with page.expect_response(
        lambda response: (
            api_url_base in response.url
            and response.request.method == "GET"
            and response.status == 200
        ),
        timeout=0,
    ) as response_info:
        page.goto(url)
        logging.info(
            "Please scan QR code or login if you haven't already. Waiting for chat to load..."
        )

    logging.info("Chat loaded! Initializing historical extraction...")
    response = response_info.value
    max_mid = None

    try:
        data = response.json()
        messages = data.get("messages", [])
        max_mid = process_messages(messages)
    except Exception as e:
        logging.error(f"Failed to parse initial response: {e}")
        return

    gid = "4761715839862414"
    source = "209678993"

    while max_mid:
        fetch_url = f"{api_url_base}?id={gid}&count=20&convert_emoji=1&query_sender=1&source={source}&max_mid={max_mid}"
        js_code = f"""
        async () => {{
            try {{
                let res = await fetch("{fetch_url}");
                if (!res.ok) return null;
                return await res.json();
            }} catch (e) {{
                return null;
            }}
        }}
        """
        try:
            hist_data = page.evaluate(js_code)
            if not hist_data:
                time.sleep(5)
                continue

            hist_messages = hist_data.get("messages", [])
            if not hist_messages:
                break

            hist_messages = [m for m in hist_messages if str(m["id"]) != str(max_mid)]
            if not hist_messages:
                break

            max_mid = process_messages(hist_messages)
            time.sleep(1.5)

        except Exception as e:
            logging.error(f"Error during historical fetch: {e}")
            break

    logging.info("Historical data extraction COMPLETED!")
    try:
        page.wait_for_timeout(36000000)
    except:
        pass
    browser.close()


if __name__ == "__main__":
    with sync_playwright() as playwright:
        run_app(playwright)
