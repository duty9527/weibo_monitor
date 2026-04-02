package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type App struct {
	configManager *ConfigManager
	tg            *TelegramClient
	weibo         *WeiboScraper
	semaphore     chan struct{}
	commandsReady bool
	nextRegister  time.Time
}

func NewApp(configPath string) (*App, error) {
	manager := NewConfigManager(configPath)
	cfg, err := manager.Load()
	if err != nil {
		return nil, err
	}
	return &App{
		configManager: manager,
		tg:            NewTelegramClient(),
		weibo:         NewWeiboScraper(),
		semaphore:     make(chan struct{}, cfg.App.MaxConcurrentScrapes),
	}, nil
}

func (a *App) Close() error {
	if a.weibo == nil {
		return nil
	}
	return a.weibo.Close()
}

func (a *App) Run(ctx context.Context) error {
	var offset int64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		cfg := a.configManager.Current()
		if !a.commandsReady && (a.nextRegister.IsZero() || time.Now().After(a.nextRegister)) {
			if err := a.tg.RegisterDefaults(ctx, cfg); err != nil {
				a.nextRegister = time.Now().Add(time.Minute)
				log.Printf("注册 Telegram 命令和菜单按钮失败，1 分钟后重试: %v", err)
			} else {
				a.commandsReady = true
				log.Printf("已注册 Telegram 命令和菜单按钮")
			}
		}

		updates, err := a.tg.GetUpdates(ctx, cfg, offset)
		if err != nil {
			log.Printf("拉取 Telegram 更新失败: %v", err)
			if err := sleepWithContext(ctx, 3*time.Second); err != nil {
				return err
			}
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			msg := update.Message
			if msg == nil {
				msg = update.EditedMessage
			}
			if msg == nil || msg.From == nil || msg.From.IsBot {
				continue
			}
			go a.handleMessage(ctx, *msg)
		}
	}
}

func (a *App) handleMessage(ctx context.Context, msg TelegramMessage) {
	command, arg, ok := parseCommand(msg.Text)
	if !ok {
		return
	}

	switch command {
	case "/start", "/help":
		cfg := a.configManager.Current()
		_ = a.tg.SendText(ctx, cfg, targetFromMessage(msg), helpText(), false)
	case "/scrape":
		if extractFirstURL(arg) == "" {
			a.runScrape(ctx, msg, arg)
			return
		}
		a.runScrape(ctx, msg, arg)
	}
}

func (a *App) runScrape(ctx context.Context, msg TelegramMessage, arg string) {
	select {
	case a.semaphore <- struct{}{}:
		defer func() { <-a.semaphore }()
	case <-ctx.Done():
		return
	}
	a.handleScrape(ctx, msg, arg)
}

func (a *App) handleScrape(ctx context.Context, msg TelegramMessage, arg string) {
	cfg := a.configManager.Current()
	target := targetFromMessage(msg)

	if !cfg.chatAllowed(msg.Chat.ID) {
		log.Printf("忽略未授权 chat_id=%d 的抓取请求", msg.Chat.ID)
		return
	}

	link := extractFirstURL(arg)
	if link == "" {
		_ = a.tg.SendText(ctx, cfg, target, "用法：/scrape 微博链接", false)
		return
	}

	_ = a.tg.SendText(ctx, cfg, target, "开始抓取，请稍候…", false)

	record, err := a.weibo.Scrape(ctx, cfg, link)
	if err != nil {
		log.Printf("抓取微博失败: %v", err)
		_ = a.tg.SendText(ctx, cfg, target, "抓取失败："+err.Error(), false)
		return
	}

	cfg = a.configManager.Current()
	if err := a.tg.SendRecord(ctx, cfg, target, record, link); err != nil {
		log.Printf("回传 Telegram 失败: %v", err)
		_ = a.tg.SendText(ctx, cfg, target, "抓取成功，但发送到 Telegram 失败："+err.Error(), false)
	}
}

func targetFromMessage(msg TelegramMessage) Target {
	return Target{
		ChatID:   msg.Chat.ID,
		ThreadID: msg.MessageThreadID,
	}
}

func helpText() string {
	return "使用方式：/scrape 微博链接"
}

func parseCommand(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || !strings.HasPrefix(text, "/") {
		return "", "", false
	}

	firstSpace := strings.IndexFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t'
	})

	command := text
	rest := ""
	if firstSpace >= 0 {
		command = text[:firstSpace]
		rest = strings.TrimSpace(text[firstSpace+1:])
	}
	if idx := strings.Index(command, "@"); idx >= 0 {
		command = command[:idx]
	}
	return command, rest, true
}

func extractFirstURL(text string) string {
	for _, field := range strings.Fields(strings.TrimSpace(text)) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			return strings.TrimRight(field, ".,)")
		}
		if strings.Contains(field, "weibo.com/") || strings.Contains(field, "weibo.cn/") || strings.Contains(field, "m.weibo.cn/") || strings.Contains(field, "t.cn/") {
			return strings.TrimRight(field, ".,)")
		}
	}
	return ""
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func main() {
	configPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	app, err := NewApp(*configPath)
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
	defer func() {
		if err := app.Close(); err != nil {
			log.Printf("关闭资源失败: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("机器人已启动，配置文件: %s", *configPath)
	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("运行失败: %v", err)
	}
	fmt.Println("机器人已退出")
}
