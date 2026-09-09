package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"weibo_group_chat_monitor/config"
	"weibo_group_chat_monitor/groupchat"
)

type mediaRetryReport struct {
	FailureRows     int      `json:"failure_rows"`
	UniqueMessages  int      `json:"unique_messages"`
	AlreadyComplete int      `json:"already_complete"`
	Recovered       int      `json:"recovered"`
	Failed          int      `json:"failed"`
	MissingHistory  int      `json:"missing_history"`
	FailureDetails  []string `json:"failure_details,omitempty"`
}

func runGroupChatMediaRetryMode(args []string) int {
	fs := flag.NewFlagSet("groupchat-media-retry", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultGroupChatConfigPath(), "配置文件路径")
	failedPath := fs.String("failed-file", "", "失败媒体审计 JSONL；留空时使用历史目录中的默认文件")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.LoadGroupChat(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}
	if strings.TrimSpace(*failedPath) == "" {
		*failedPath = filepath.Join(mediaRetryHistoryDir(cfg.Output.HistoryFile), "failed_media_messages.jsonl")
	} else if !filepath.IsAbs(*failedPath) {
		*failedPath = filepath.Join(filepath.Dir(*configPath), *failedPath)
	}
	cookies, err := groupchat.LoadStoredCookies(cfg.Subscription.CookieCacheFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取 Cookie 失败: %v\n", err)
		return 1
	}
	report, err := retryFailedGroupChatMedia(cfg, cookies, *failedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "回填失败媒体失败: %v\n", err)
		return 1
	}
	encoded, _ := json.Marshal(report)
	fmt.Println(string(encoded))
	if report.Failed > 0 || report.MissingHistory > 0 {
		return 1
	}
	return 0
}

func retryFailedGroupChatMedia(cfg *config.GroupChatModeConfig, cookies []groupchat.StoredCookie, failedPath string) (mediaRetryReport, error) {
	report := mediaRetryReport{}
	failures, err := loadFailedMessages(failedPath, &report)
	if err != nil {
		return report, err
	}
	records, err := loadRetryHistory(cfg.Output.HistoryFile, failures)
	if err != nil {
		return report, err
	}
	downloader := groupchat.NewRealtimeMediaDownloader(cfg, cookies)
	updates := make([]groupchat.OutputRecord, 0, len(failures))
	ids := make([]string, 0, len(failures))
	for id := range failures {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record, exists := records[id]
		if !exists {
			report.MissingHistory++
			continue
		}
		if retryRecordMediaComplete(record) {
			report.AlreadyComplete++
			continue
		}
		paths, downloadFailures := downloader.DownloadMessage(failures[id])
		if len(downloadFailures) > 0 || len(paths) == 0 {
			report.Failed++
			for _, failure := range downloadFailures {
				report.FailureDetails = append(report.FailureDetails, fmt.Sprintf("%s/%s: %v", id, failure.MediaRef, failure.Err))
			}
			if len(downloadFailures) == 0 {
				report.FailureDetails = append(report.FailureDetails, id+": 未生成媒体文件")
			}
			continue
		}
		joined := strings.Join(paths, ", ")
		record.DownloadedMedia = &joined
		record.HasImage = true
		updates = append(updates, record)
		report.Recovered++
	}
	if err := groupchat.MergeRecords(cfg.Output.HistoryFile, updates); err != nil {
		return report, err
	}
	return report, nil
}

func loadFailedMessages(path string, report *mediaRetryReport) (map[string]groupchat.ChatMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	messages := make(map[string]groupchat.ChatMessage)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		report.FailureRows++
		var failure groupchat.FailedMediaRecord
		if err := json.Unmarshal(scanner.Bytes(), &failure); err != nil {
			return nil, err
		}
		id := strings.TrimSpace(failure.MessageID)
		if id == "" || len(failure.RawMessage) == 0 {
			continue
		}
		var message groupchat.ChatMessage
		if err := json.Unmarshal(failure.RawMessage, &message); err != nil {
			return nil, fmt.Errorf("解析消息 %s 原始数据失败: %w", id, err)
		}
		messages[id] = message
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	report.UniqueMessages = len(messages)
	return messages, nil
}

func loadRetryHistory(historyPath string, wanted map[string]groupchat.ChatMessage) (map[string]groupchat.OutputRecord, error) {
	dir := mediaRetryHistoryDir(historyPath)
	files, err := filepath.Glob(filepath.Join(dir, "????-??-??.jsonl"))
	if err != nil {
		return nil, err
	}
	records := make(map[string]groupchat.OutputRecord)
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var record groupchat.OutputRecord
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				file.Close()
				return nil, fmt.Errorf("解析历史文件 %s 失败: %w", path, err)
			}
			if _, ok := wanted[record.ID]; ok {
				records[record.ID] = record
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return records, nil
}

func mediaRetryHistoryDir(historyPath string) string {
	if strings.EqualFold(filepath.Ext(historyPath), ".jsonl") {
		return filepath.Dir(historyPath)
	}
	return historyPath
}

func retryRecordMediaComplete(record groupchat.OutputRecord) bool {
	if record.DownloadedMedia == nil || strings.TrimSpace(*record.DownloadedMedia) == "" {
		return false
	}
	for _, path := range strings.Split(*record.DownloadedMedia, ",") {
		if _, err := os.Stat(strings.TrimSpace(path)); err != nil {
			return false
		}
	}
	return true
}
