package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	// ActionCancelDeny cancels deny-with-message prompt.
	ActionCancelDeny = "deny_cancel"
	// ActionDelete deletes a resolved message.
	ActionDelete = "delete"
)

// Handler processes Telegram updates and resolves approvals.
type Handler struct {
	bot         *telego.Bot
	registry    *approvals.Registry
	messages    map[string]i18n.Messages
	defaultLang string
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
func NewHandler(bot *telego.Bot, registry *approvals.Registry, messages map[string]i18n.Messages, defaultLang string, chatID int64, sttLang string, transcriber Transcriber, log *slog.Logger) *Handler {
	return &Handler{
		bot:         bot,
		registry:    registry,
		messages:    messages,
		defaultLang: defaultLang,
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
		_ = h.answerCallback(ctx, query, h.messageFor("").InvalidChat)
		return
	}
	action, payload := parseCallback(query.Data)

	switch action {
	case ActionApprove:
		h.resolveDecision(ctx, query, payload, approvals.DecisionApprove, "approved")
	case ActionDeny:
		h.resolveDecision(ctx, query, payload, approvals.DecisionDeny, "denied")
	case ActionDenyWithMessage:
		h.startDenyPrompt(ctx, query, payload)
	case ActionCancelDeny:
		h.cancelDenyPrompt(ctx, query, payload)
	case ActionDelete:
		h.deleteMessage(ctx, query, payload)
	default:
		_ = h.answerCallback(ctx, query, h.messageFor("").InvalidAction)
	}
}

func (h *Handler) handleMessage(ctx context.Context, message *telego.Message) {
	if !h.allowedChat(message.Chat.ID) {
		return
	}
	approval, _ := h.registry.CurrentPrompt()
	if approval == nil || !approval.AwaitingReason {
		return
	}
	if message.Text != "" {
		reason := strings.TrimSpace(message.Text)
		if reason == "" {
			reason = "denied"
		}
		approval, promptID, ok := h.registry.Resolve(approval.Request.CorrelationID)
		if !ok {
			return
		}
		if promptID > 0 {
			_ = h.DeleteMessage(ctx, promptID)
		}
		h.FinalizeApproval(ctx, approval, approvals.Result{Decision: approvals.DecisionDeny, Reason: reason}, "")
		return
	}
	if message.Voice != nil {
		reason, err := h.transcribeVoice(ctx, message.Voice)
		if err != nil {
			if errors.Is(err, errTranscriberDisabled) {
				_ = h.reply(ctx, h.messageFor(approval.Request.Lang).VoiceDisabled)
			} else {
				_ = h.reply(ctx, h.messageFor(approval.Request.Lang).TranscriptionFailed)
			}
			return
		}
		if strings.TrimSpace(reason) == "" {
			reason = "denied"
		}
		approval, promptID, ok := h.registry.Resolve(approval.Request.CorrelationID)
		if !ok {
			return
		}
		if promptID > 0 {
			_ = h.DeleteMessage(ctx, promptID)
		}
		h.FinalizeApproval(ctx, approval, approvals.Result{Decision: approvals.DecisionDeny, Reason: reason}, "")
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

func (h *Handler) deleteMessage(ctx context.Context, query *telego.CallbackQuery, payload string) {
	messageID, err := strconv.Atoi(payload)
	if err != nil || messageID <= 0 {
		_ = h.answerCallback(ctx, query, h.messageFor("").InvalidAction)
		return
	}
	_ = h.DeleteMessage(ctx, messageID)
	_ = h.answerCallback(ctx, query, "")
}

// CallbackData builds callback data for an action.
func CallbackData(action, payload string) string {
	if payload == "" {
		return action
	}
	return action + ":" + payload
}

func parseCallback(data string) (string, string) {
	parts := strings.SplitN(data, ":", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (h *Handler) resolveDecision(ctx context.Context, query *telego.CallbackQuery, correlationID string, decision approvals.Decision, reason string) {
	approval, promptID, ok := h.registry.Resolve(correlationID)
	if !ok {
		_ = h.answerCallback(ctx, query, h.messageFor("").AlreadyResolved)
		return
	}
	if promptID > 0 {
		_ = h.DeleteMessage(ctx, promptID)
	}
	h.FinalizeApproval(ctx, approval, approvals.Result{Decision: decision, Reason: reason}, "")
	msg := h.messageFor(approval.Request.Lang)
	switch decision {
	case approvals.DecisionApprove:
		_ = h.answerCallback(ctx, query, msg.ApprovedNote)
	case approvals.DecisionDeny:
		_ = h.answerCallback(ctx, query, msg.DeniedNote)
	default:
		_ = h.answerCallback(ctx, query, msg.ErrorNote)
	}
}

func (h *Handler) startDenyPrompt(ctx context.Context, query *telego.CallbackQuery, correlationID string) {
	approval := h.registry.Get(correlationID)
	if approval == nil {
		_ = h.answerCallback(ctx, query, h.messageFor("").AlreadyResolved)
		return
	}
	prevPromptID, ok := h.registry.StartReason(correlationID)
	if !ok {
		_ = h.answerCallback(ctx, query, h.messageFor(approval.Request.Lang).AlreadyResolved)
		return
	}
	if prevPromptID > 0 {
		_ = h.DeleteMessage(ctx, prevPromptID)
	}
	msg := h.messageFor(approval.Request.Lang)
	prompt, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    tu.ID(h.chatID),
		Text:      msg.DenyPrompt,
		ParseMode: parseMode(approval.Request.Markup),
		ReplyParameters: (&telego.ReplyParameters{
			MessageID: approval.MessageID,
		}).WithAllowSendingWithoutReply(),
		ReplyMarkup: h.promptKeyboard(approval.Request.Lang, approval.Request.CorrelationID),
	})
	if err != nil {
		h.log.Error("Failed to send deny prompt", "error", err)
		_ = h.answerCallback(ctx, query, msg.ErrorNote)
		return
	}
	h.registry.SetPromptMessage(correlationID, prompt.MessageID)
	_ = h.answerCallback(ctx, query, "")
}

func (h *Handler) cancelDenyPrompt(ctx context.Context, query *telego.CallbackQuery, correlationID string) {
	promptID := h.registry.ClearPrompt(correlationID)
	if promptID > 0 {
		_ = h.DeleteMessage(ctx, promptID)
	}
	_ = h.answerCallback(ctx, query, "")
}

// FinalizeApproval updates the approval message and sends a webhook callback.
func (h *Handler) FinalizeApproval(ctx context.Context, approval *approvals.Approval, result approvals.Result, timeoutMessage string) {
	msg := h.messageFor(approval.Request.Lang)
	note := h.noteForResult(msg, result, timeoutMessage)
	text := approval.MessageText
	if strings.TrimSpace(note) != "" {
		text = fmt.Sprintf("%s\n\n%s", approval.MessageText, note)
	}
	_, err := h.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      tu.ID(h.chatID),
		MessageID:   approval.MessageID,
		Text:        text,
		ParseMode:   parseMode(approval.Request.Markup),
		ReplyMarkup: h.resolvedKeyboard(approval.Request.Lang, approval.MessageID),
	})
	if err != nil {
		h.log.Error("Failed to update telegram message", "error", err)
	}
	h.sendWebhook(ctx, approval, result)
}

// DeleteMessage removes a Telegram message.
func (h *Handler) DeleteMessage(ctx context.Context, messageID int) error {
	if messageID <= 0 {
		return nil
	}
	err := h.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    tu.ID(h.chatID),
		MessageID: messageID,
	})
	return err
}

func (h *Handler) sendWebhook(ctx context.Context, approval *approvals.Approval, result approvals.Result) {
	if approval == nil {
		return
	}
	if strings.TrimSpace(approval.Request.Callback.URL) == "" {
		return
	}
	payload := map[string]any{
		"correlation_id": approval.Request.CorrelationID,
		"decision":       string(result.Decision),
		"reason":         result.Reason,
		"tool":           approval.Request.Tool,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, approval.Request.Callback.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	if _, err := client.Do(req); err != nil {
		h.log.Error("Webhook delivery failed", "error", err, "correlation_id", approval.Request.CorrelationID)
	}
}

func (h *Handler) messageFor(lang string) i18n.Messages {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = h.defaultLang
	}
	if msg, ok := h.messages[lang]; ok {
		return msg
	}
	if msg, ok := h.messages["en"]; ok {
		return msg
	}
	return i18n.Messages{}
}

func (h *Handler) noteForResult(msg i18n.Messages, result approvals.Result, timeoutMessage string) string {
	switch result.Decision {
	case approvals.DecisionApprove:
		return msg.ApprovedNote
	case approvals.DecisionDeny:
		if strings.TrimSpace(result.Reason) != "" && result.Reason != "denied" {
			return fmt.Sprintf("%s\n%s", msg.DeniedNote, result.Reason)
		}
		return msg.DeniedNote
	case approvals.DecisionError:
		if strings.TrimSpace(result.Reason) == "approval timeout" {
			if strings.TrimSpace(timeoutMessage) != "" {
				return timeoutMessage
			}
			return msg.TimeoutNote
		}
		if strings.TrimSpace(result.Reason) != "" {
			return fmt.Sprintf("⚠️ %s", result.Reason)
		}
		return msg.ErrorNote
	default:
		return ""
	}
}

func (h *Handler) promptKeyboard(lang, correlationID string) *telego.InlineKeyboardMarkup {
	msg := h.messageFor(lang)
	cancel := CallbackData(ActionCancelDeny, correlationID)
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(msg.CancelDenyButton).WithCallbackData(cancel),
		),
	)
}

func (h *Handler) resolvedKeyboard(lang string, messageID int) *telego.InlineKeyboardMarkup {
	msg := h.messageFor(lang)
	del := CallbackData(ActionDelete, strconv.Itoa(messageID))
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(msg.DeleteButton).WithCallbackData(del),
		),
	)
}

func parseMode(markup string) string {
	switch strings.ToLower(strings.TrimSpace(markup)) {
	case "html":
		return telego.ModeHTML
	default:
		return telego.ModeMarkdown
	}
}
