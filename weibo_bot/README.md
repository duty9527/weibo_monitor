# Weibo Telegram Bot

Go 编写的 Telegram 机器人。收到 `/scrape 微博链接` 后，会调用微博接口抓取正文、长文本、转发内容和媒体文件，并将结果回传到当前聊天或话题。
启动后会自动注册 `/scrape`、`/help` 命令，并把私聊输入框菜单按钮设置为命令列表。

## 启动

1. 复制 `config.example.json` 为 `config.json`
2. 在 `config.json` 中填写 `telegram.bot_token`
3. 准备一个已经登录微博的 Playwright 持久化目录，默认是 `weibo_user_data`
   可以直接复用你原来 Python 脚本使用的那个目录
4. 安装 Playwright Chromium 运行时：

```bash
go run github.com/playwright-community/playwright-go/cmd/playwright install chromium
```

5. 运行：

```bash
go run .
```

## 配置

- `telegram.allowed_chat_ids` 留空表示所有 chat 都允许
- `weibo.cookie_source` 默认为 `playwright`，会以无头 Playwright 打开 `weibo.user_data_dir` 并提取 Cookie
- `weibo.cookie_source` 也可改成 `static`，回退到 `weibo.cookie` 或 `weibo.cookie_file`
- `weibo.user_data_dir` 应当是独立的 Playwright 用户目录，不要复用日常 Chrome 默认目录
- `weibo.download_media` 控制是否下载图片/视频
- `weibo.save_record` 控制是否落盘 `weibo_<id>.json`

## 命令

```text
/scrape https://weibo.com/1401527553/NB4vXy3aP
```

```text
/help
```
