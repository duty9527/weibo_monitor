package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	mode, remaining, err := parseModeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usageText())
		return 2
	}

	switch mode {
	case "weibo":
		return runWeiboMode(remaining)
	case "groupchat":
		return runGroupChatMode(remaining)
	default:
		fmt.Fprintf(os.Stderr, "不支持的运行模式: %s\n\n%s", mode, usageText())
		return 2
	}
}

func parseModeArgs(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("缺少运行模式")
	}

	first := strings.TrimSpace(args[0])
	if first != "" && !strings.HasPrefix(first, "-") {
		return normalizeMode(first), args[1:], nil
	}

	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "-mode" && i+1 < len(args) {
			remaining := append([]string{}, args[:i]...)
			remaining = append(remaining, args[i+2:]...)
			return normalizeMode(args[i+1]), remaining, nil
		}
		if strings.HasPrefix(arg, "-mode=") {
			remaining := append([]string{}, args[:i]...)
			remaining = append(remaining, args[i+1:]...)
			return normalizeMode(strings.TrimPrefix(arg, "-mode=")), remaining, nil
		}
	}

	return "", nil, fmt.Errorf("缺少运行模式")
}

func normalizeMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "group_chat", "group-chat", "groupchat":
		return "groupchat"
	default:
		return value
	}
}

func usageText() string {
	return `用法:
  go run . weibo -config config.weibo.yaml
  go run . groupchat -config config.groupchat.yaml
  go run . -mode=weibo
  go run . -mode=groupchat
`
}
