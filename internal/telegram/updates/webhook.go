package updates

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/mymmrac/telego"
)

// Webhook delivers Telegram updates via HTTP webhook.
type Webhook struct {
	bot     *telego.Bot
	url     string
	secret  string
	updates chan telego.Update
	closed  atomic.Bool
	log     *slog.Logger
}

// NewWebhook creates a new webhook source.
func NewWebhook(bot *telego.Bot, url, secret string, log *slog.Logger) *Webhook {
	return &Webhook{
		bot:     bot,
		url:     url,
		secret:  secret,
		updates: make(chan telego.Update, 128),
		log:     log,
	}
}

// Start sets webhook on Telegram side.
func (w *Webhook) Start(ctx context.Context) error {
	params := &telego.SetWebhookParams{
		URL:         w.url,
		SecretToken: w.secret,
		AllowedUpdates: []string{
			telego.MessageUpdates,
			telego.CallbackQueryUpdates,
		},
	}
	if err := w.bot.SetWebhook(ctx, params); err != nil {
		return err
	}
	w.log.Info("Telegram updates started via webhook", "url", w.url)
	return nil
}

// Stop removes the webhook.
func (w *Webhook) Stop(ctx context.Context) error {
	w.closed.Store(true)
	return w.bot.DeleteWebhook(ctx, &telego.DeleteWebhookParams{DropPendingUpdates: true})
}

// Updates returns the updates channel.
func (w *Webhook) Updates() <-chan telego.Update {
	return w.updates
}

// Handler returns HTTP handler for Telegram webhook updates.
func (w *Webhook) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if w.closed.Load() {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodPost {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		secret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if secret != w.secret {
			w.log.Warn("Webhook secret mismatch")
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		defer r.Body.Close()
		decoder := json.NewDecoder(r.Body)
		var update telego.Update
		if err := decoder.Decode(&update); err != nil {
			w.log.Error("Failed to decode webhook update", "error", err)
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case w.updates <- update:
			rw.WriteHeader(http.StatusOK)
		default:
			w.log.Error("Webhook update dropped: queue full")
			rw.WriteHeader(http.StatusServiceUnavailable)
		}
	})
}
