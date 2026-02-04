package updates

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mymmrac/telego"
)

// LongPolling delivers Telegram updates via long polling.
type LongPolling struct {
	bot     *telego.Bot
	updates <-chan telego.Update
	log     *slog.Logger
}

// NewLongPolling creates a new long polling source.
func NewLongPolling(bot *telego.Bot, log *slog.Logger) *LongPolling {
	return &LongPolling{bot: bot, log: log}
}

// Start initializes long polling updates.
func (l *LongPolling) Start(ctx context.Context) error {
	params := &telego.GetUpdatesParams{
		Timeout: 10,
		AllowedUpdates: []string{
			telego.MessageUpdates,
			telego.CallbackQueryUpdates,
		},
	}
	updates, err := l.bot.UpdatesViaLongPolling(ctx, params)
	if err != nil {
		return err
	}
	l.updates = updates
	l.log.Info("Telegram updates started via long polling")
	return nil
}

// Updates returns the updates channel.
func (l *LongPolling) Updates() <-chan telego.Update {
	return l.updates
}

// Stop stops long polling.
func (l *LongPolling) Stop(context.Context) error {
	return nil
}

// Handler is not used for long polling.
func (l *LongPolling) Handler() http.Handler {
	return nil
}
