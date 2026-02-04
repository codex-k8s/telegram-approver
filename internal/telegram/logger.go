package telegram

import (
	"fmt"
	"log/slog"
)

// telegoLogger adapts slog logger to telego.Logger.
type telegoLogger struct {
	log *slog.Logger
}

func (l telegoLogger) Debugf(format string, args ...any) {
	l.log.Debug("telegram", "message", formatMessage(format, args...))
}

func (l telegoLogger) Errorf(format string, args ...any) {
	l.log.Error("telegram", "message", formatMessage(format, args...))
}

func formatMessage(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
