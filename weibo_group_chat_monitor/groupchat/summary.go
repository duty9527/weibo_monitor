package groupchat

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type SenderSummary struct {
	Sender  string
	Header  string
	Entries []SenderSummaryEntry
}

type SenderSummaryEntry struct {
	Text       string
	MediaPaths []string
}

func BuildSenderSummaries(now time.Time, records []OutputRecord, filters []string) []SenderSummary {
	return buildSenderSummaries(records, filters, func(sender string, senderRecords []OutputRecord) string {
		return FormatSenderSummaryHeader(now, sender, len(senderRecords))
	})
}

func BuildLocalHistorySenderSummaries(records []OutputRecord, filters []string) []SenderSummary {
	return buildSenderSummaries(records, filters, func(sender string, senderRecords []OutputRecord) string {
		return FormatLocalHistorySummaryHeader(sender, senderRecords)
	})
}

func buildSenderSummaries(
	records []OutputRecord,
	filters []string,
	headerFn func(sender string, senderRecords []OutputRecord) string,
) []SenderSummary {
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

	summaries := make([]SenderSummary, 0, len(senders))
	for _, sender := range senders {
		senderRecords := append([]OutputRecord(nil), grouped[sender]...)
		sortOutputRecords(senderRecords)
		entries := make([]SenderSummaryEntry, 0, len(senderRecords))
		for _, record := range senderRecords {
			entries = append(entries, SenderSummaryEntry{
				Text:       formatSenderSummaryEntry(record),
				MediaPaths: splitMediaPaths(record.DownloadedMedia),
			})
		}
		summaries = append(summaries, SenderSummary{
			Sender:  sender,
			Header:  headerFn(sender, senderRecords),
			Entries: entries,
		})
	}
	return summaries
}

func BuildSenderSummaryMessages(now time.Time, records []OutputRecord, filters []string) []string {
	summaries := BuildSenderSummaries(now, records, filters)
	messages := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		lines := []string{summary.Header}
		for _, entry := range summary.Entries {
			lines = append(lines, entry.Text)
		}
		messages = append(messages, strings.Join(lines, "\n"))
	}
	return messages
}

func FormatSenderSummary(now time.Time, sender string, records []OutputRecord) string {
	lines := []string{FormatSenderSummaryHeader(now, sender, len(records))}

	for _, record := range records {
		lines = append(lines, formatSenderSummaryEntry(record))
	}
	return strings.Join(lines, "\n")
}

func FormatSenderSummaryHeader(now time.Time, sender string, count int) string {
	return strings.Join([]string{
		now.In(time.Local).Format("2006年01月02日"),
		fmt.Sprintf("%s发送了%d条消息，分别是：", strings.TrimSpace(sender), count),
	}, "\n")
}

func FormatLocalHistorySummaryHeader(sender string, records []OutputRecord) string {
	count := len(records)
	if count == 0 {
		return strings.Join([]string{
			"本地历史筛选结果",
			fmt.Sprintf("%s发送了0条消息。", strings.TrimSpace(sender)),
		}, "\n")
	}

	firstDate := summaryHeaderDate(records[0])
	lastDate := summaryHeaderDate(records[len(records)-1])
	rangeText := firstDate
	if firstDate != "" && lastDate != "" && firstDate != lastDate {
		rangeText = firstDate + " 至 " + lastDate
	}
	if rangeText == "" {
		rangeText = "时间范围未知"
	}

	return strings.Join([]string{
		"本地历史筛选结果",
		fmt.Sprintf("%s发送了%d条消息，时间范围：%s", strings.TrimSpace(sender), count, rangeText),
	}, "\n")
}

func summaryHeaderDate(record OutputRecord) string {
	if parsed, ok := record.ParsedTime(); ok {
		return parsed.Format("2006-01-02")
	}
	return strings.TrimSpace(record.Date)
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
	// if len(splitMediaPaths(record.DownloadedMedia)) > 0 || record.HasImage {
	// if text == "" {
	// 	return "[图片]"
	// }
	// return text + "[图片]"

	// }
	if text == "" {
		return "[空消息]"
	}
	return text
}

func formatSenderSummaryEntry(record OutputRecord) string {
	return fmt.Sprintf("%s %s", summaryLineTime(record), summaryLineText(record))
}
