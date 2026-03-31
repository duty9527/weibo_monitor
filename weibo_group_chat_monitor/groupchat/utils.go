package groupchat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"weibo_group_chat_monitor/config"

	playwright "github.com/playwright-community/playwright-go"
)

var (
	emojiPattern    = regexp.MustCompile(`\[([^\[\]]+)\]`)
	urlPattern      = regexp.MustCompile(`https?://[^\s　，、《》“”]+`)
	atPattern       = regexp.MustCompile(`@([\p{Han}\w_-]+)`)
	retractPattern  = regexp.MustCompile(`.+撤回了一条消息$`)
	dateFilePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)
)

var systemKeywords = []string{
	"加入了群聊", "退出了群聊", "被移出群聊",
	"修改了群名", "群公告", "被设为管理员",
	"已成为新群主", "开启了全员禁言", "关闭了全员禁言",
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}

	return strings.Map(func(r rune) rune {
		switch {
		case r < 32:
			return '_'
		case strings.ContainsRune(`\/:*?"<>|`, r):
			return '_'
		default:
			return r
		}
	}, name)
}

func normalizeMediaURL(url string) string {
	url = strings.TrimSpace(url)
	switch {
	case strings.HasPrefix(url, "//"):
		return "https:" + url
	case strings.HasPrefix(url, "http:"):
		return "https:" + strings.TrimPrefix(url, "http:")
	default:
		return url
	}
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

func joinMediaPaths(paths []string) *string {
	paths = uniqueStrings(paths)
	if len(paths) == 0 {
		return nil
	}
	joined := strings.Join(paths, ", ")
	return &joined
}

func matchesStopCondition(cond config.StopCondition, readableTime, sender, text string) bool {
	if !cond.Enabled {
		return false
	}
	if strings.TrimSpace(cond.TargetTime) == "" &&
		strings.TrimSpace(cond.TargetSender) == "" &&
		strings.TrimSpace(cond.TargetMessage) == "" {
		return false
	}

	timeOK := true
	if strings.TrimSpace(cond.TargetTime) != "" {
		timeOK = matchesStopTime(cond.TargetTime, readableTime)
	}
	senderOK := cond.TargetSender == "" || strings.Contains(sender, cond.TargetSender)
	messageOK := cond.TargetMessage == "" || strings.Contains(text, cond.TargetMessage)
	return timeOK && senderOK && messageOK
}

func matchesStopTime(targetTime, readableTime string) bool {
	targetBoundary, ok := parseStopTimeBoundary(targetTime)
	if !ok {
		return strings.Contains(readableTime, targetTime)
	}

	messageTime, err := time.ParseInLocation(outputTimeLayout, strings.TrimSpace(readableTime), time.Local)
	if err != nil {
		return strings.Contains(readableTime, targetTime)
	}

	return !messageTime.After(targetBoundary)
}

func parseStopTimeBoundary(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}

	if parsed, err := time.ParseInLocation(outputTimeLayout, value, time.Local); err == nil {
		return parsed, true
	}
	if parsed, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func loadSeenIDs(path string) (map[string]struct{}, error) {
	seen := make(map[string]struct{})
	path = strings.TrimSpace(path)
	if path == "" {
		return seen, nil
	}

	if filepath.Ext(path) == ".jsonl" {
		if err := loadSeenIDsFromFile(path, seen); err != nil {
			return nil, err
		}
	}

	outputDir := historyOutputDir(path)
	entries, err := os.ReadDir(outputDir)
	if os.IsNotExist(err) {
		return seen, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取历史目录失败: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !dateFilePattern.MatchString(entry.Name()) {
			continue
		}
		if err := loadSeenIDsFromFile(filepath.Join(outputDir, entry.Name()), seen); err != nil {
			return nil, err
		}
	}

	return seen, nil
}

func appendRecords(path string, records []OutputRecord) error {
	if len(records) == 0 {
		return nil
	}
	outputDir := historyOutputDir(path)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("创建历史输出目录失败: %w", err)
	}

	grouped := make(map[string][]OutputRecord)
	for _, record := range records {
		dateStr := normalizeRecordDate(record)
		grouped[dateStr] = append(grouped[dateStr], record)
	}

	dates := make([]string, 0, len(grouped))
	for dateStr := range grouped {
		dates = append(dates, dateStr)
	}
	sort.Strings(dates)

	for _, dateStr := range dates {
		dailyRecords := grouped[dateStr]
		sortOutputRecords(dailyRecords)

		filePath := filepath.Join(outputDir, dateStr+".jsonl")
		file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("打开历史文件失败: %w", err)
		}

		for _, record := range dailyRecords {
			line, err := json.Marshal(record)
			if err != nil {
				file.Close()
				return fmt.Errorf("序列化输出记录失败: %w", err)
			}
			if _, err := fmt.Fprintf(file, "%s\n", line); err != nil {
				file.Close()
				return fmt.Errorf("写入历史文件失败: %w", err)
			}
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("关闭历史文件失败: %w", err)
		}
	}
	return nil
}

func loadSeenIDsFromFile(path string, seen map[string]struct{}) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开历史文件失败: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var record OutputRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if strings.TrimSpace(record.ID) != "" {
			seen[record.ID] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取历史文件失败: %w", err)
	}
	return nil
}

func historyOutputDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "."
	}
	if filepath.Ext(path) == ".jsonl" {
		dir := filepath.Dir(path)
		if dir == "" {
			return "."
		}
		return dir
	}
	return path
}

func normalizeRecordDate(record OutputRecord) string {
	if dateStr := strings.TrimSpace(record.Date); dateStr != "" {
		return dateStr
	}
	if parsed, ok := record.ParsedTime(); ok {
		return parsed.Format("2006-01-02")
	}
	return "unknown-date"
}

func failedMediaRecordPath(historyPath string) string {
	return filepath.Join(historyOutputDir(historyPath), "failed_media_messages.jsonl")
}

func buildOutputRecord(msg ChatMessage, readableTime, sender, text string, mediaPaths []string) OutputRecord {
	dateStr, hour := deriveDateHour(readableTime)
	hasImage := msg.HasImage()

	return OutputRecord{
		ID:              msg.IDString(),
		Time:            readableTime,
		Date:            dateStr,
		Hour:            hour,
		Sender:          sender,
		Message:         text,
		MsgType:         classifyMessage(text, hasImage),
		TextClean:       cleanText(text),
		Emojis:          extractEmojis(text),
		URLs:            extractURLs(text),
		MentionedUsers:  extractMentionedUsers(text),
		DownloadedMedia: joinMediaPaths(mediaPaths),
		HasImage:        hasImage,
	}
}

func deriveDateHour(readableTime string) (string, int) {
	parsed, err := time.ParseInLocation(outputTimeLayout, strings.TrimSpace(readableTime), time.Local)
	if err != nil {
		if len(readableTime) >= 10 {
			return readableTime[:10], -1
		}
		return "", -1
	}
	return parsed.Format("2006-01-02"), parsed.Hour()
}

func classifyMessage(message string, hasImage bool) string {
	message = strings.TrimSpace(message)
	if message == "" && hasImage {
		return "image"
	}
	if message == "" {
		return "empty"
	}
	if retractPattern.MatchString(message) {
		return "retracted"
	}
	for _, keyword := range systemKeywords {
		if strings.Contains(message, keyword) {
			return "system"
		}
	}
	if hasImage || message == "分享图片" {
		return "image"
	}
	if urlPattern.MatchString(message) {
		return "link"
	}
	if strings.HasPrefix(message, "@") {
		return "at_mention"
	}
	return "text"
}

func extractEmojis(message string) []string {
	matches := emojiPattern.FindAllStringSubmatch(message, -1)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		result = append(result, strings.TrimSpace(match[1]))
	}
	return uniqueStrings(result)
}

func extractURLs(message string) []string {
	return uniqueStrings(urlPattern.FindAllString(message, -1))
}

func extractMentionedUsers(message string) []string {
	matches := atPattern.FindAllStringSubmatch(message, -1)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		result = append(result, strings.TrimSpace(match[1]))
	}
	return uniqueStrings(result)
}

func cleanText(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	message = emojiPattern.ReplaceAllString(message, "")
	message = urlPattern.ReplaceAllString(message, "")
	return strings.TrimSpace(message)
}

func sortOutputRecords(records []OutputRecord) {
	sort.Slice(records, func(i, j int) bool {
		return outputRecordLess(records[i], records[j])
	})
}

func outputRecordLess(left, right OutputRecord) bool {
	leftTime, leftOK := left.ParsedTime()
	rightTime, rightOK := right.ParsedTime()
	if leftOK && rightOK && !leftTime.Equal(rightTime) {
		return leftTime.Before(rightTime)
	}

	leftID, leftIDOK := messageIDValue(left.ID)
	rightID, rightIDOK := messageIDValue(right.ID)
	if leftIDOK && rightIDOK && leftID != rightID {
		return leftID < rightID
	}

	if left.Time != right.Time {
		return left.Time < right.Time
	}
	return left.ID < right.ID
}

func chatMessageLess(left, right ChatMessage) bool {
	leftTime := left.TimeValue(time.Now())
	rightTime := right.TimeValue(time.Now())
	if !leftTime.Equal(rightTime) {
		return leftTime.Before(rightTime)
	}

	leftID, leftOK := messageIDValue(left.IDString())
	rightID, rightOK := messageIDValue(right.IDString())
	if leftOK && rightOK && leftID != rightID {
		return leftID < rightID
	}
	return left.IDString() < right.IDString()
}

func isAfterStateBoundary(state *RunState, msg ChatMessage) bool {
	if state == nil {
		return true
	}

	msgID := strings.TrimSpace(msg.IDString())
	stateID := strings.TrimSpace(state.LastMessageID)
	if msgID != "" && stateID != "" {
		if msgID == stateID {
			return false
		}
		msgValue, msgOK := messageIDValue(msgID)
		stateValue, stateOK := messageIDValue(stateID)
		if msgOK && stateOK {
			return msgValue > stateValue
		}
	}

	boundaryTime, ok := state.LastMessageParsedTime()
	if !ok {
		return true
	}
	return msg.TimeValue(time.Now()).After(boundaryTime)
}

func matchesTargetSender(sender string, filters []string) bool {
	sender = strings.TrimSpace(sender)
	if sender == "" || len(filters) == 0 {
		return false
	}

	for _, filter := range filters {
		filter = strings.TrimSpace(filter)
		if filter == "" {
			continue
		}
		if sender == filter || strings.Contains(sender, filter) {
			return true
		}
	}
	return false
}

func normalizeInlineText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.Join(strings.Fields(value), " ")
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func timeoutMillisPointer(seconds int) *float64 {
	ms := float64(seconds * 1000)
	return &ms
}

func evaluateInto(page playwright.Page, expression string, arg any, target any) error {
	var (
		value any
		err   error
	)
	if arg == nil {
		value, err = page.Evaluate(expression)
	} else {
		value, err = page.Evaluate(expression, arg)
	}
	if err != nil {
		return err
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

func messageIDValue(id string) (int64, bool) {
	if id == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}
