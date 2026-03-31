package groupchat

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func BuildSenderSummaryMessages(now time.Time, records []OutputRecord, filters []string) []string {
	grouped := make(map[string][]OutputRecord)
	for _, record := range records {
		if !matchesTargetSender(record.Sender, filters) {
			continue
		}
		grouped[record.Sender] = append(grouped[record.Sender], record)
	}
	if len(grouped) == 0 {
		return nil
	}

	senders := make([]string, 0, len(grouped))
	for sender := range grouped {
		senders = append(senders, sender)
	}
	sort.Strings(senders)

	messages := make([]string, 0, len(senders))
	for _, sender := range senders {
		records := append([]OutputRecord(nil), grouped[sender]...)
		sortOutputRecords(records)
		messages = append(messages, FormatSenderSummary(now, sender, records))
	}
	return messages
}

func FormatSenderSummary(now time.Time, sender string, records []OutputRecord) string {
	lines := []string{
		now.In(time.Local).Format("2006年01月02日"),
		fmt.Sprintf("%s发送了%d条消息，分别是：", strings.TrimSpace(sender), len(records)),
	}

	for _, record := range records {
		lines = append(lines, fmt.Sprintf("%s %s", summaryLineTime(record), summaryLineText(record)))
	}
	return strings.Join(lines, "\n")
}

func summaryLineTime(record OutputRecord) string {
	parsed, ok := record.ParsedTime()
	if !ok {
		return strings.TrimSpace(record.Time)
	}
	return parsed.Format("15:04:05")
}

func summaryLineText(record OutputRecord) string {
	text := normalizeInlineText(record.Message)
	if record.HasImage {
		if text == "" {
			return "[图片]"
		}
		return text + "[图片]"
	}
	if text == "" {
		return "[空消息]"
	}
	return text
}
