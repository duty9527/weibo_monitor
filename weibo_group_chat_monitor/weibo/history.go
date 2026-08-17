package weibo

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// LoadLocalHistoryRecords 加载本地微博历史文件的所有记录。
func LoadLocalHistoryRecords(historyFile string) ([]*WeiboRecord, error) {
	records := make([]*WeiboRecord, 0)
	f, err := os.Open(historyFile)
	if os.IsNotExist(err) {
		return records, nil
	}
	if err != nil {
		return nil, fmt.Errorf("打开微博历史文件失败: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var rec WeiboRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		records = append(records, &rec)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取微博历史文件失败: %w", err)
	}

	return records, nil
}
