package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/codex-k8s/telegram-approver/internal/approvals"
	"github.com/codex-k8s/telegram-approver/internal/i18n"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	// ActionApprove approves the request.
	ActionApprove = "approve"
	// ActionDeny denies the request.
	ActionDeny = "deny"
	// ActionDenyWithMessage requests a denial reason.
	ActionDenyWithMessage = "deny_reason"
)

// Handler processes Telegram updates and resolves approvals.
type Handler struct {
	bot         *telego.Bot
	registry    *approvals.Registry
	messages    i18n.Messages
	chatID      int64
	sttLang     string
	transcriber Transcriber
	log         *slog.Logger
}

// Transcriber converts audio to text.
type Transcriber interface {
	Transcribe(ctx context.Context, reader io.Reader, filename, contentType, language string) (string, error)
}

// NewHandler creates a new update handler.
func NewHandler(bot *telego.Bot, registry *approvals.Registry, messages i18n.Messages, chatID int64, sttLang string, transcriber Transcriber, log *slog.Logger) *Handler {
	return &Handler{
		bot:         bot,
		registry:    registry,
		messages:    messages,
		chatID:      chatID,
		sttLang:     sttLang,
		transcriber: transcriber,
		log:         log,
	}
}

// Run processes updates until context cancellation.
func (h *Handler) Run(ctx context.Context, updates <-chan telego.Update) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			h.HandleUpdate(ctx, update)
		}
	}
}

// HandleUpdate processes a single update.
func (h *Handler) HandleUpdate(ctx context.Context, update telego.Update) {
	if update.CallbackQuery != nil {
		h.handleCallback(ctx, update.CallbackQuery)
		return
	}
	if update.Message != nil {
		h.handleMessage(ctx, update.Message)
		return
	}
}

func (h *Handler) handleCallback(ctx context.Context, query *telego.CallbackQuery) {
	if query.Message == nil {
		return
	}
	if !h.allowedChat(query.Message.GetChat().ID) {
		_ = h.answerCallback(ctx, query, h.messages.InvalidChat)
		return
	}
	action, correlationID := parseCallback(query.Data)
	approval := h.registry.Active()
	if approval == nil {
		_ = h.answerCallback(ctx, query, h.messages.AlreadyResolved)
		return
	}
	if correlationID != "" && correlationID != approval.Request.CorrelationID {
		_ = h.answerCallback(ctx, query, h.messages.AlreadyResolved)
		return
	}

	switch action {
	case ActionApprove:
		if !h.registry.Resolve(approval, approvals.DecisionApprove, "approved") {
			_ = h.answerCallback(ctx, query, h.messages.AlreadyResolved)
			return
		}
		_ = h.answerCallback(ctx, query, h.messages.ApprovedNote)
	case ActionDeny:
		if !h.registry.Resolve(approval, approvals.DecisionDeny, "denied") {
			_ = h.answerCallback(ctx, query, h.messages.AlreadyResolved)
			return
		}
		_ = h.answerCallback(ctx, query, h.messages.DeniedNote)
	case ActionDenyWithMessage:
		if !h.registry.MarkAwaitingReason(approval) {
			_ = h.answerCallback(ctx, query, h.messages.AlreadyResolved)
			return
		}
		if err := h.removeKeyboard(ctx, approval.MessageID); err != nil {
			h.log.Error("Failed to remove keyboard", "error", err)
		}
		_, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:    tu.ID(h.chatID),
			Text:      h.messages.DenyPrompt,
			ParseMode: telego.ModeMarkdown,
		})
		if err != nil {
			h.log.Error("Failed to send deny prompt", "error", err)
		}
		_ = h.answerCallback(ctx, query, "")
	default:
		_ = h.answerCallback(ctx, query, h.messages.InvalidAction)
	}
}

func (h *Handler) handleMessage(ctx context.Context, message *telego.Message) {
	if !h.allowedChat(message.Chat.ID) {
		return
	}
	approval := h.registry.Active()
	if approval == nil || !approval.AwaitingReason {
		return
	}
	if message.Text != "" {
		reason := strings.TrimSpace(message.Text)
		if reason == "" {
			reason = "denied"
		}
		h.registry.Resolve(approval, approvals.DecisionDeny, reason)
		return
	}
	if message.Voice != nil {
		reason, err := h.transcribeVoice(ctx, message.Voice)
		if err != nil {
			if errors.Is(err, errTranscriberDisabled) {
				_ = h.reply(ctx, h.messages.VoiceDisabled)
			} else {
				_ = h.reply(ctx, h.messages.TranscriptionFailed)
			}
			return
		}
		if strings.TrimSpace(reason) == "" {
			reason = "denied"
		}
		h.registry.Resolve(approval, approvals.DecisionDeny, reason)
		return
	}
}

func (h *Handler) transcribeVoice(ctx context.Context, voice *telego.Voice) (string, error) {
	if h.transcriber == nil {
		return "", errTranscriberDisabled
	}
	file, err := h.bot.GetFile(ctx, &telego.GetFileParams{FileID: voice.FileID})
	if err != nil {
		return "", err
	}
	audioURL := h.bot.FileDownloadURL(file.FilePath)
	data, err := tu.DownloadFile(audioURL)
	if err != nil {
		return "", err
	}
	normalized, mimeType, fileName, err := normalizeVoiceAudio(ctx, data, "", file.FilePath)
	if err != nil {
		return "", err
	}
	reader := bytes.NewReader(normalized)
	return h.transcriber.Transcribe(ctx, reader, fileName, mimeType, h.sttLang)
}

var errTranscriberDisabled = errors.New("transcriber disabled")

func (h *Handler) allowedChat(chatID int64) bool {
	return chatID == h.chatID
}

func (h *Handler) answerCallback(ctx context.Context, query *telego.CallbackQuery, text string) error {
	params := &telego.AnswerCallbackQueryParams{CallbackQueryID: query.ID}
	if strings.TrimSpace(text) != "" {
		params.Text = text
	}
	return h.bot.AnswerCallbackQuery(ctx, params)
}

func (h *Handler) reply(ctx context.Context, text string) error {
	_, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    tu.ID(h.chatID),
		Text:      text,
		ParseMode: telego.ModeMarkdown,
	})
	return err
}

func (h *Handler) removeKeyboard(ctx context.Context, messageID int) error {
	_, err := h.bot.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{
		ChatID:      tu.ID(h.chatID),
		MessageID:   messageID,
		ReplyMarkup: emptyKeyboard(),
	})
	return err
}

func emptyKeyboard() *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}}
}

// CallbackData builds callback data for an action.
func CallbackData(action, correlationID string) string {
	if correlationID == "" {
		return action
	}
	return action + ":" + correlationID
}

func parseCallback(data string) (string, string) {
	parts := strings.SplitN(data, ":", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}
