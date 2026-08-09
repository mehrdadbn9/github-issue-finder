package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// truncateString shortens s to maxLen characters, counting runes rather than
// bytes. Slicing by byte splits any multi-byte character that straddles the cut
// and leaves a partial rune behind, which Telegram rejects outright: two alerts
// in one cycle were lost to "Bad Request: text must be encoded in UTF-8"
// because an issue title was cut mid-character at 80 bytes.
func truncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func maskDBConn(conn string) string {
	if conn == "" {
		return "(empty)"
	}
	re := regexp.MustCompile(`(password=)([^ ]+)`)
	return re.ReplaceAllStringFunc(conn, func(m string) string {
		return "password=[REDACTED]"
	})
}

// parseTelegramChatIDs parses chat IDs from env vars. Returns all valid IDs
// from TELEGRAM_CHAT_ID (singular) and TELEGRAM_CHAT_IDS (plural/comma-separated).
func parseTelegramChatIDs(singular, plural string) []int64 {
	seen := map[int64]bool{}
	var ids []int64
	for _, raw := range []string{plural, singular} {
		if raw == "" {
			continue
		}
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			parsed, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				continue
			}
			if !seen[parsed] {
				seen[parsed] = true
				ids = append(ids, parsed)
			}
		}
	}
	return ids
}
