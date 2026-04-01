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
		records := append([]OutputRecord(nil), grouped[sender]...)
		sortOutputRecords(records)
		entries := make([]SenderSummaryEntry, 0, len(records))
		for _, record := range records {
			entries = append(entries, SenderSummaryEntry{
				Text:       formatSenderSummaryEntry(record),
				MediaPaths: splitMediaPaths(record.DownloadedMedia),
			})
		}
		summaries = append(summaries, SenderSummary{
			Sender:  sender,
			Header:  FormatSenderSummaryHeader(now, sender, len(records)),
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

func summaryLineTime(record OutputRecord) string {
	parsed, ok := record.ParsedTime()
	if !ok {
		return strings.TrimSpace(record.Time)
	}
	return parsed.Format("15:04:05")
}

func summaryLineText(record OutputRecord) string {
	text := normalizeInlineText(record.Message)
	if len(splitMediaPaths(record.DownloadedMedia)) > 0 || record.HasImage {
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

func formatSenderSummaryEntry(record OutputRecord) string {
	return fmt.Sprintf("%s %s", summaryLineTime(record), summaryLineText(record))
}
