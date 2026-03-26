import time
import base64
import os
from playwright.sync_api import sync_playwright

def download_via_browser_evaluate():
    print("1. 启动 Playwright 加载历史登录状态 ...")
    
    with sync_playwright() as p:
        # 以有头模式打开，保证 Weibo 的 JS 能够正常解析
        browser = p.chromium.launch_persistent_context("./weibo_user_data", headless=False)
        page = browser.pages[0] if browser.pages else browser.new_page()
        
        # 跳转到聊天页面，确保在正确的域名下（从而让 fetch 带上所有安全凭证和 cors 头）
        print("2. 正在访问微博以加载凭证环境...")
        page.goto("https://api.weibo.com/chat/#/chat")
        
        # 等待页面加载好 Cookie 状态
        time.sleep(4)
        
        fid = "5274925147619636"
        touid = "4761715839862414"
        url = f"https://upload.api.weibo.com/2/mss/msget?fid={fid}&touid={touid}"
        
        print(f"3. 正在从浏览器底层执行强制底层 fetch 抓取图片: {url}")
        
        js_download_code = f"""
        async () => {{
            try {{
                // 使用原生的 fetch，浏览器会自动为我们附带当前的真实 Cookie 和跨域伪装
                const response = await fetch("{url}", {{
                    method: 'GET',
                    headers: {{
                        'Referer': 'https://api.weibo.com/chat/'
                    }}
                }});
                
                if (!response.ok) {{
                    return {{ success: false, status: response.status, text: await response.text() }};
                }}
                
                const contentType = response.headers.get('content-type');
                // 以 Blob 流接收二进制图片
                const blob = await response.blob();
                
                // 为了能将二进制传回 Python，我们将其转换为 Base64 字符串
                return new Promise((resolve, reject) => {{
                    const reader = new FileReader();
                    reader.onloadend = () => {{
                        resolve({{ 
                            success: true, 
                            contentType: contentType, 
                            base64data: reader.result 
                        }});
                    }};
                    reader.onerror = reject;
                    reader.readAsDataURL(blob);
                }});
            }} catch (e) {{
                return {{ success: false, error: e.toString() }};
            }}
        }}
        """
        
        try:
            result = page.evaluate(js_download_code)
            
            if result.get("success"):
                base64_str = result.get("base64data")
                # FileReader 返回的数据格式类似: "data:image/jpeg;base64,/9j/4AAQSkZJ..."
                # 我们需要剥离前缀，只保留真正的 base64
                header, encoded = base64_str.split(",", 1)
                
                # 判断文件类型
                ext = 'jpg'
                if 'png' in header: ext = 'png'
                elif 'gif' in header: ext = 'gif'
                
                filename = f"image_via_browser_{fid}.{ext}"
                file_data = base64.b64decode(encoded)
                
                with open(filename, 'wb') as f:
                    f.write(file_data)
                    
                size_kb = os.path.getsize(filename) / 1024
                print(f"✅ 提取成功！由于强制使用了浏览器的安全上下文，图片被顺利拽回，大小为: {size_kb:.2f} KB，已保存为: {filename}")
            else:
                print("❌ 提取失败：", result)
                
        except Exception as e:
            print("❌ 执行 JS 异常：", e)
            
        finally:
            browser.close()

if __name__ == "__main__":
    download_via_browser_evaluate()
