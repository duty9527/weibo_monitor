import time
import json
import logging
import sys
from playwright.sync_api import sync_playwright

logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(message)s')

def run_app(playwright):
    user_data_dir = "./weibo_user_data"
    
    browser = playwright.chromium.launch_persistent_context(
        user_data_dir,
        headless=False,
        viewport={"width": 1280, "height": 800}
    )
    
    page = browser.pages[0] if browser.pages else browser.new_page()
    url = "https://api.weibo.com/chat/#/chat?check_gid=4761715839862414&source_from=11"
    api_url_base = "https://api.weibo.com/webim/groupchat/query_messages.json"
    
    found = False
    window_max_mid = [None]

    def handle_response(response):
        nonlocal found
        if found:
            return
            
        if "query_messages.json" in response.url:
            try:
                data = response.json()
                if not data:
                    return
                messages = data.get('messages', [])
                if messages and not window_max_mid[0]:
                    window_max_mid[0] = str(min(messages, key=lambda x: int(x['id']))['id'])
                    
                for msg in messages:
                    raw_dump = json.dumps(msg, ensure_ascii=False)
                    if '"pic"' in raw_dump or 'sinaimg.cn' in raw_dump or '[图片]' in raw_dump:
                        logging.info("=============== FOUND RAW IMAGE MESSAGE ===============")
                        with open("raw_image_msg.json", "w", encoding="utf-8") as f:
                            json.dump(msg, f, ensure_ascii=False, indent=4)
                        logging.info("Saved raw message to raw_image_msg.json")
                        logging.info("=======================================================")
                        found = True
                        return
            except Exception as e:
                pass

    page.on("response", handle_response)
    page.goto(url)
    
    # Wait for the initial request to complete
    time.sleep(5)
    
    gid = "4761715839862414"
    source = "209678993"
    
    max_mid = window_max_mid[0]
    
    while max_mid and not found:
        logging.info(f"Digging deeper for image before {max_mid}...")
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
                time.sleep(2)
                continue
                
            hist_messages = hist_data.get('messages', [])
            if not hist_messages:
                break
                
            for msg in hist_messages:
                if str(msg['id']) == str(max_mid):
                   continue
                raw_dump = json.dumps(msg, ensure_ascii=False)
                if '"pic"' in raw_dump or 'sinaimg.cn' in raw_dump or '[图片]' in raw_dump:
                    logging.info("=============== FOUND RAW IMAGE MESSAGE ===============")
                    with open("raw_image_msg.json", "w", encoding="utf-8") as f:
                        json.dump(msg, f, ensure_ascii=False, indent=4)
                    logging.info("Saved raw message to raw_image_msg.json")
                    logging.info("=======================================================")
                    found = True
                    break
                    
            if not found:
                max_mid = min(hist_messages, key=lambda x: int(x['id']))['id']
            time.sleep(1)
        except Exception as e:
            logging.error(f"Error: {e}")
            break
            
    if not found:
         logging.info("Could not find image message in immediate history.")
    browser.close()

if __name__ == "__main__":
    with sync_playwright() as playwright:
        run_app(playwright)
