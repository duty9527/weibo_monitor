# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Project Overview

微博群聊监控与抓取工具集。核心功能包括：通过 Playwright 抓取微博群聊消息、按指定用户微博号抓取博文、数据清洗和看板展示。项目包含 Python 脚本集和两个 Go 子项目。

## Directory Structure

- `weibo_monitor_go/` — Go 实现的微博博文监控推送，支持增量抓取、Cookie 保活、媒体去重、Telegram 推送
- `group_chat_monitor/` — Go 实现的微博群聊监控，抓取群聊记录并筛选特定发送者消息推送到 Telegram
- Root-level Python scripts — 早期/辅助脚本，包括 Playwright 抓取、数据清洗、看板生成

## Go Projects

### weibo_monitor_go

```
module weibo_monitor
go 1.24.0
deps: playwright-go, go-sqlite3, yaml.v3
```

Packages:
- `config/` — YAML 配置加载（`config.yaml`），包含微博 Cookie/目标 UID、Telegram token/chat_id、日志级别
- `weibo/` — 核心抓取逻辑：`scraper.go`（API 请求+SQLite 存储）、`cookie.go`（Playwright 登录保活）、`state.go`（抓取/媒体发送状态管理）、`types.go`（数据结构）、`media_dedupe.go`（基于文件指纹的去重）、`utils.go`（Netscape Cookie 解析+时间解析）
- `telegram/` — Telegram Bot API 客户端，支持图文发送+媒体去重

Entry point: `main.go`。通过 `-config` 指定配置文件，默认查找 `config.yaml`。

Data flow: Cookie 加载/刷新 -> 调用微博 API 获取新博文 -> 媒体去重 -> Telegram 推送 -> SQLite 持久化

### group_chat_monitor

```
module group_chat_monitor
go 1.24.0
deps: playwright-go, yaml.v3
```

Packages:
- `config/` — YAML 配置加载
- `groupchat/` — 群聊抓取：`scraper.go`（通过 Playwright 注入 JS 调用群聊 API）、`types.go`（数据结构）、`utils.go`（工具函数）、`types_test.go`（测试）

Entry point: `main.go`。通过 `-config` 指定配置文件，默认查找 `config.yaml`。

Data flow: Playwright 打开微博群聊页面 -> 注入 JS 调用 `query_messages.json` API -> 解析消息 -> 媒体下载 -> 筛选特定发送者 -> Telegram 推送

## Python Scripts

| 脚本 | 功能 |
|------|------|
| `monitor.py` | 通过 Playwright 抓取微博群聊历史消息到 `clean_history.jsonl` |
| `monitor_with_images.py` | 带图片下载的群聊抓取 |
| `scrape_weibo_user.py` | 按指定 UID 抓取微博用户博文 |
| `scrape_all_weibo.py` | 抓取多个用户的全部博文 |
| `scrape_weibo_link.py` | 从单条微博链接抓取内容和媒体 |
| `browser_fetch.py` | 浏览器 Cookie 提取辅助脚本 |
| `get_raw_image_msg.py` | 提取图片消息 |
| `data_clean.py` | 清洗 `clean_history.jsonl` -> `cleaned_data.jsonl`，含去重、分类、统计报告 |
| `generate_dashboard_data.py` | 将清洗数据转为 `dashboard_data.json` 供看板使用 |
| `dashboard.html` | 本地数据看板 |

## Commands

### Python 环境
```bash
source .venv/bin/activate  # 激活虚拟环境
```

### 群聊抓取（Python）
```bash
python monitor.py                     # 抓取群聊历史到 clean_history.jsonl
python monitor_with_images.py         # 带图片下载的群聊抓取
python data_clean.py                  # 清洗 + 去重 + 统计报告
python generate_dashboard_data.py     # 生成看板数据
```

### 单条微博抓取（Python）
```bash
python scrape_weibo_link.py <weibo_url>  # 按链接抓取单条微博
```

### 微博监控推送（Go）
```bash
cd weibo_monitor_go && go run .           # 使用 config.yaml 运行
cd weibo_monitor_go && go run . -config /path/to/config.yaml
cd weibo_monitor_go && go test ./...      # 运行测试
```

### 群聊监控（Go）
```bash
cd group_chat_monitor && go run .           # 使用 config.yaml 运行
cd group_chat_monitor && go run . -config /path/to/config.yaml
```

## Key Configuration

- `config.yaml` / `weibo_monitor_go/config.yaml` / `group_chat_monitor/config.yaml` — YAML 配置，包含微博 Cookie/目标 UID、Telegram Bot token、日志级别等。参考 `config.example.yaml`
- `weibo_user_data/` — Playwright 浏览器持久化目录（登录会话），不要提交到版本控制
- `*.jsonl` — 运行生成的数据文件，不要提交到版本控制
- `media_downloads/` — 下载的媒体文件，不要提交到版本控制
