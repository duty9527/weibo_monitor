package groupchat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type LocalHistoryReadOptions struct {
	TargetSenders []string
	StartDate     string
	EndDate       string
	MaxRecords    int
}

func LoadLocalHistoryRecords(historyPath string, opts LocalHistoryReadOptions) ([]OutputRecord, error) {
	startDate, _ := parseDateOnly(strings.TrimSpace(opts.StartDate))
	endDate, _ := parseDateOnly(strings.TrimSpace(opts.EndDate))

	files, err := listLocalHistoryFiles(historyPath, startDate, endDate)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	records := make([]OutputRecord, 0)
	for _, path := range files {
		if err := loadLocalHistoryFile(path, opts, startDate, endDate, seen, &records); err != nil {
			return nil, err
		}
	}

	sortOutputRecords(records)
	if opts.MaxRecords > 0 && len(records) > opts.MaxRecords {
		records = append([]OutputRecord(nil), records[len(records)-opts.MaxRecords:]...)
	}
	return records, nil
}

func listLocalHistoryFiles(historyPath string, startDate, endDate time.Time) ([]string, error) {
	historyPath = strings.TrimSpace(historyPath)
	if historyPath == "" {
		return nil, fmt.Errorf("history path 不能为空")
	}

	files := make([]string, 0)
	added := make(map[string]struct{})
	addFile := func(path string) {
		path = filepath.Clean(path)
		if _, ok := added[path]; ok {
			return
		}
		added[path] = struct{}{}
		files = append(files, path)
	}

	if filepath.Ext(historyPath) == ".jsonl" {
		if info, err := os.Stat(historyPath); err == nil && !info.IsDir() {
			base := filepath.Base(historyPath)
			if !dateFilePattern.MatchString(base) || dateFileInRange(base, startDate, endDate) {
				addFile(historyPath)
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取历史文件失败: %w", err)
		}
	}

	outputDir := historyOutputDir(historyPath)
	entries, err := os.ReadDir(outputDir)
	if os.IsNotExist(err) {
		sort.Strings(files)
		return files, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取历史目录失败: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !dateFilePattern.MatchString(entry.Name()) {
			continue
		}
		if !dateFileInRange(entry.Name(), startDate, endDate) {
			continue
		}
		addFile(filepath.Join(outputDir, entry.Name()))
	}

	sort.Strings(files)
	return files, nil
}

func loadLocalHistoryFile(
	path string,
	opts LocalHistoryReadOptions,
	startDate, endDate time.Time,
	seen map[string]struct{},
	records *[]OutputRecord,
) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开历史文件失败: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var record OutputRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return fmt.Errorf("解析历史记录失败: %s:%d: %w", path, lineNo, err)
		}
		if !matchesTargetSender(record.Sender, opts.TargetSenders) {
			continue
		}
		if !recordDateInRange(record, startDate, endDate) {
			continue
		}

		id := strings.TrimSpace(record.ID)
		if id != "" {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
		}
		*records = append(*records, record)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取历史文件失败: %w", err)
	}
	return nil
}

func dateFileInRange(name string, startDate, endDate time.Time) bool {
	dateStr := strings.TrimSuffix(strings.TrimSpace(name), ".jsonl")
	dateValue, ok := parseDateOnly(dateStr)
	if !ok {
		return false
	}
	return dateInRange(dateValue, startDate, endDate)
}

func recordDateInRange(record OutputRecord, startDate, endDate time.Time) bool {
	if startDate.IsZero() && endDate.IsZero() {
		return true
	}

	dateStr := strings.TrimSpace(record.Date)
	if dateStr != "" {
		if dateValue, ok := parseDateOnly(dateStr); ok {
			return dateInRange(dateValue, startDate, endDate)
		}
	}

	parsed, ok := record.ParsedTime()
	if !ok {
		return true
	}
	dateValue := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.Local)
	return dateInRange(dateValue, startDate, endDate)
}

func dateInRange(value, startDate, endDate time.Time) bool {
	if !startDate.IsZero() && value.Before(startDate) {
		return false
	}
	if !endDate.IsZero() && value.After(endDate) {
		return false
	}
	return true
}

func parseDateOnly(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
