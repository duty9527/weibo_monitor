package weibo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// FilterSentMedia 过滤已经发送过的媒体，返回用于发送的记录副本和本次新媒体指纹。
func FilterSentMedia(record *WeiboRecord, state *RunState) (*WeiboRecord, []string, error) {
	if record == nil {
		return nil, nil, nil
	}

	clone := *record
	if len(record.LocalMediaPaths) == 0 {
		return &clone, nil, nil
	}

	filteredPaths := make([]string, 0, len(record.LocalMediaPaths))
	newMediaKeys := make([]string, 0, len(record.LocalMediaPaths))
	seenInRecord := make(map[string]struct{}, len(record.LocalMediaPaths))

	for _, path := range record.LocalMediaPaths {
		key, err := mediaFingerprint(path)
		if err != nil {
			return nil, nil, fmt.Errorf("path %s: %w", path, err)
		}
		if key == "" {
			continue
		}
		if _, ok := seenInRecord[key]; ok {
			continue
		}
		seenInRecord[key] = struct{}{}
		if state != nil && state.HasSentMedia(key) {
			continue
		}

		filteredPaths = append(filteredPaths, path)
		newMediaKeys = append(newMediaKeys, key)
	}

	clone.LocalMediaPaths = filteredPaths
	return &clone, newMediaKeys, nil
}

func mediaFingerprint(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开媒体文件失败: %w", err)
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", fmt.Errorf("读取媒体文件失败: %w", err)
	}

	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}
