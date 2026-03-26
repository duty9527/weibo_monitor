package weibo

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// copyFile 复制文件（处理 SQLite 数据库锁定问题）
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// ReadNetscapeCookies 读取 Netscape 格式的 cookies.txt 文件
// 格式: domain\tinclude_subdomains\tpath\tsecure\texpiry\tname\tvalue
func ReadNetscapeCookies(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var parts []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过注释和空行
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := fields[0]
		name := fields[5]
		value := fields[6]
		// 只保留微博和新浪相关域名的 Cookie
		if strings.Contains(domain, "weibo.com") || strings.Contains(domain, "sina.com") {
			if name != "" && value != "" {
				parts = append(parts, name+"="+value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(parts, "; "), nil
}

var configTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
}

var weiboTimeLayouts = []string{
	time.RubyDate,
	"Mon Jan 02 15:04:05 -0700 2006",
	"Mon Jan _2 15:04:05 -0700 2006",
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// ParseConfigTime 解析配置文件中的时间节点。
func ParseConfigTime(value string) (time.Time, error) {
	return parseWithLayouts(value, configTimeLayouts)
}

// ParseWeiboTime 解析微博 API 返回的 created_at 字段。
func ParseWeiboTime(value string) (time.Time, error) {
	return parseWithLayouts(value, weiboTimeLayouts)
}

func parseWithLayouts(value string, layouts []string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("time value is empty")
	}

	var lastErr error
	for _, layout := range layouts {
		var (
			t   time.Time
			err error
		)
		if strings.Contains(layout, "MST") || strings.Contains(layout, "-0700") || strings.Contains(layout, "Z07") {
			t, err = time.Parse(layout, value)
		} else {
			t, err = time.ParseInLocation(layout, value, time.Local)
		}
		if err == nil {
			return t, nil
		}
		lastErr = err
	}

	return time.Time{}, fmt.Errorf("unsupported time format %q: %w", value, lastErr)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
