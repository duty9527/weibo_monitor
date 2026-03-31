import json
import logging
from playwright.sync_api import sync_playwright

def test():
    user_data_dir = "./weibo_user_data"
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch_persistent_context(
            user_data_dir, headless=True, viewport={"width": 1280, "height": 800}
        )
        page = browser.pages[0] if browser.pages else browser.new_page()
        page.goto("https://weibo.com/ajax/statuses/show?id=5282569198506507")
        page.wait_for_timeout(2000)
        print("URL:", page.url)
        print("CONTENT:", page.locator("body").inner_text())
        browser.close()

test()
