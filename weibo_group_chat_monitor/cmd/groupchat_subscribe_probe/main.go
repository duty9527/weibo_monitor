package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"weibo_group_chat_monitor/groupchat"
	"weibo_group_chat_monitor/weibo"
)

func main() {
	var (
		uid           = flag.String("uid", "", "当前登录微博 UID；留空时通过 Cookie 自动查询")
		cookieFile    = flag.String("cookie-file", "", "Netscape cookies.txt 路径；也可使用 WEIBO_COOKIE 环境变量")
		endpoint      = flag.String("endpoint", groupchat.DefaultBayeuxEndpoint, "Bayeux 服务地址")
		duration      = flag.Duration("duration", 2*time.Minute, "监听时长，0 表示持续运行")
		handshakeOnly = flag.Bool("handshake-only", false, "只验证服务握手，不订阅")
		insecure      = flag.Bool("insecure", false, "仅测试：跳过 TLS 证书校验")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	cookieHeader, err := loadCookie(*cookieFile)
	if err != nil {
		fatal(err)
	}
	if !*handshakeOnly && cookieHeader == "" {
		fatal(fmt.Errorf("订阅需要认证：设置 WEIBO_COOKIE 或 -cookie-file"))
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if *insecure {
		fmt.Fprintln(os.Stderr, "警告：已跳过 TLS 证书校验，仅应用于本次协议测试")
	}
	transport.TLSClientConfig, err = groupchat.NewWeiboIMTLSConfig(*insecure)
	if err != nil {
		fatal(err)
	}
	httpClient := &http.Client{Transport: transport, Jar: jar}
	client, err := groupchat.NewBayeuxClient(*endpoint, cookieHeader, httpClient)
	if err != nil {
		fatal(err)
	}

	handshake, err := client.Handshake(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("握手成功: client_id=%s transports=%v\n", handshake.ClientID, handshake.SupportedConnectionTypes)
	if *handshakeOnly {
		return
	}

	currentUID := strings.TrimSpace(*uid)
	if currentUID == "" {
		currentUID, err = groupchat.QueryCurrentUID(ctx, cookieHeader, httpClient)
		if err != nil {
			fatal(err)
		}
	}
	channel := "/im/" + currentUID
	fmt.Printf("开始订阅 %s；等待实时事件……\n", channel)

	if err := client.Subscribe(ctx, channel); err != nil {
		fatal(err)
	}
	err = client.Receive(ctx, channel, func(message groupchat.BayeuxMessage) {
		payload, marshalErr := json.Marshal(message)
		if marshalErr != nil {
			fmt.Fprintf(os.Stderr, "事件编码失败: %v\n", marshalErr)
			return
		}
		fmt.Printf("事件 %s\n", payload)
	})
	if err != nil {
		fatal(err)
	}
	fmt.Println("监听结束")
}

func loadCookie(path string) (string, error) {
	if value := strings.TrimSpace(os.Getenv("WEIBO_COOKIE")); value != "" {
		return value, nil
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	value, err := weibo.ReadNetscapeCookies(path)
	if err != nil {
		return "", fmt.Errorf("读取 Cookie 文件失败: %w", err)
	}
	return value, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
