所有项目文件编写在当前的目录中，项目目标:
1. 作为telegram机器人的后台，当收到 /scrape [link] 时直接爬取[link]中的所有内容，并发送到聊天框
2. 项目代码使用golang编写
3. 抓取方案参考 [scrape_weibo_link.py](scrape_weibo_link.py)
4. 程序通过读取配置文件获取动态配置
5. 给你最大的权限，直到完成整个项目