package shared

import "strings"

// EscapeHTML escapes text for Telegram HTML mode.
func EscapeHTML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}

// EscapeMarkdownV2 escapes text for Telegram MarkdownV2 mode.
func EscapeMarkdownV2(value string) string {
	return escapeWithSet(value, "_*[]()~`>#+-=|{}.!\\")
}

// EscapeMarkdownV2Code escapes inline code payload for Telegram MarkdownV2 mode.
func EscapeMarkdownV2Code(value string) string {
	return escapeWithSet(value, "\\`")
}

// EscapeMarkdownV2URL escapes URL part of markdown links.
func EscapeMarkdownV2URL(value string) string {
	return escapeWithSet(value, "\\)")
}

func escapeWithSet(value, escapedRunes string) string {
	if value == "" {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value) * 2)
	for _, r := range value {
		if strings.ContainsRune(escapedRunes, r) {
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
