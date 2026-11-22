package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/weibo"
)

// Runs authentication and the WebIM handshake without receiving or sending messages.
func runGroupChatAuthMode(args []string) int {
	fs := flag.NewFlagSet("groupchat-auth", flag.ContinueOnError)
	path := fs.String("config", defaultGroupChatConfigPath(), "群聊配置")
	refresh := fs.Bool("refresh", false, "访问主站刷新持久化登录态")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.LoadGroupChat(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	session, err := groupchat.AcquireAuthSession(ctx, cfg, newLogger("info"), *refresh)
	if err == nil {
		err = groupchat.ValidateBayeuxHandshake(ctx, session.Cookies, cfg.Subscription.TLSInsecureSkipVerify)
	}
	if err != nil {
		newLogger("info").Error("群聊认证检查失败", "err", err)
		return 1
	}
	newLogger("info").Info("群聊登录与订阅握手验证成功", "uid", session.UID)
	return 0
}

func runWeiboAuthMode(args []string) int {
	fs := flag.NewFlagSet("weibo-auth", flag.ContinueOnError)
	path := fs.String("config", defaultWeiboConfigPath(), "微博配置")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.LoadWeibo(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	logger := newLogger("info")
	cookie, _, verified, err := loadWeiboCookies(ctx, cfg, logger)
	if err == nil && !verified {
		if verifyErr := weibo.VerifyCookiesDetailed(ctx, cookie, cfg.Weibo.TargetUID); verifyErr != nil {
			err = fmt.Errorf("微博 Cookie 校验失败: %w", verifyErr)
		}
	}
	if err != nil {
		logger.Error("微博认证检查失败", "err", err)
		return 1
	}
	logger.Info("微博认证检查完成")
	return 0
}
