package shared

import (
	"strings"

	"github.com/codex-k8s/telegram-approver/internal/i18n"
)

// MessagesFor resolves localized messages with fallback to configured default and then English.
func MessagesFor(messages map[string]i18n.Messages, lang, fallbackLang string) i18n.Messages {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = strings.ToLower(strings.TrimSpace(fallbackLang))
	}
	if msg, ok := messages[lang]; ok {
		return msg
	}
	if msg, ok := messages["en"]; ok {
		return msg
	}
	return i18n.Messages{}
}
