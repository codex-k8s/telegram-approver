package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/codex-k8s/telegram-approver/internal/approvals"
	"github.com/codex-k8s/telegram-approver/internal/config"
	"github.com/codex-k8s/telegram-approver/internal/i18n"
	"github.com/codex-k8s/telegram-approver/internal/telegram/handlers"
	"github.com/codex-k8s/telegram-approver/internal/telegram/updates"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

const timeoutReason = "approval timeout"

// Service manages Telegram bot lifecycle and approval requests.
type Service struct {
	bot      *telego.Bot
	source   updates.Source
	handler  *handlers.Handler
	registry *approvals.Registry
	log      *slog.Logger
	messages i18n.Messages
	chatID   int64
}

// New creates a new Telegram service.
func New(cfg config.Config, bundle i18n.Bundle, registry *approvals.Registry, log *slog.Logger) (*Service, error) {
	bot, err := telego.NewBot(cfg.Token, telego.WithLogger(telegoLogger{log: log}))
	if err != nil {
		return nil, err
	}

	var source updates.Source
	if cfg.WebhookEnabled() {
		source = updates.NewWebhook(bot, cfg.WebhookURL, cfg.WebhookSecret, log)
	} else {
		source = updates.NewLongPolling(bot, log)
	}

	var transcriber handlers.Transcriber
	if cfg.OpenAIAPIKey != "" {
		transcriber = handlers.NewOpenAITranscriber(cfg.OpenAIAPIKey, cfg.STTModel, cfg.STTTimeout, log)
	}

	sttLang := cfg.Lang
	if sttLang == "" {
		sttLang = "en"
	}

	handler := handlers.NewHandler(bot, registry, bundle.Messages, cfg.ChatID, sttLang, transcriber, log)

	return &Service{
		bot:      bot,
		source:   source,
		handler:  handler,
		registry: registry,
		log:      log,
		messages: bundle.Messages,
		chatID:   cfg.ChatID,
	}, nil
}

// Start begins receiving Telegram updates.
func (s *Service) Start(ctx context.Context) error {
	if err := s.source.Start(ctx); err != nil {
		return err
	}
	go s.handler.Run(ctx, s.source.Updates())
	return nil
}

// Stop shuts down Telegram update processing.
func (s *Service) Stop(ctx context.Context) error {
	return s.source.Stop(ctx)
}

// WebhookHandler returns the webhook HTTP handler if enabled.
func (s *Service) WebhookHandler() http.Handler {
	return s.source.Handler()
}

// RequestApproval sends approval request to Telegram and waits for a decision.
func (s *Service) RequestApproval(ctx context.Context, req approvals.Request, timeout time.Duration, timeoutMessage string) (approvals.Result, error) {
	if timeout <= 0 {
		timeout = time.Hour
	}
	deadline := time.Now().Add(timeout)
	acquireCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	if err := s.registry.Acquire(acquireCtx); err != nil {
		return approvals.Result{Decision: approvals.DecisionError, Reason: timeoutReason}, nil
	}
	defer s.registry.Release()

	approval, err := s.registry.Start(req)
	if err != nil {
		return approvals.Result{Decision: approvals.DecisionError, Reason: "approver busy"}, nil
	}
	defer s.registry.Clear(approval)

	messageText := s.renderMessage(req)
	keyboard := s.approvalKeyboard(req.CorrelationID)

	msg, err := s.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      tu.ID(s.chatID),
		Text:        messageText,
		ParseMode:   telego.ModeMarkdown,
		ReplyMarkup: keyboard,
	})
	if err != nil {
		s.log.Error("Failed to send telegram message", "error", err)
		return approvals.Result{Decision: approvals.DecisionError, Reason: "failed to send telegram message"}, err
	}

	s.registry.SetMessage(approval, msg.MessageID, messageText)

	remaining := time.Until(deadline)
	if remaining <= 0 {
		remaining = time.Second
	}

	result := s.registry.Wait(ctx, approval, remaining, timeoutReason)
	s.updateMessage(ctx, approval, result, timeoutMessage)
	return result, nil
}

func (s *Service) renderMessage(req approvals.Request) string {
	payload, err := json.MarshalIndent(req.Arguments, "", "  ")
	if err != nil {
		payload = []byte("{}")
	}
	builder := &strings.Builder{}
	builder.WriteString("*")
	builder.WriteString(s.messages.ApprovalTitle)
	builder.WriteString("*\n\n")
	builder.WriteString(s.messages.ApprovalCorrelation)
	builder.WriteString(": `")
	builder.WriteString(req.CorrelationID)
	builder.WriteString("`\n")
	builder.WriteString(s.messages.ApprovalTool)
	builder.WriteString(": `")
	builder.WriteString(req.Tool)
	builder.WriteString("`\n\n")
	builder.WriteString(s.messages.ApprovalParams)
	builder.WriteString(":\n\n")
	builder.WriteString("```json\n")
	builder.Write(payload)
	builder.WriteString("\n```")
	return builder.String()
}

func (s *Service) approvalKeyboard(correlationID string) *telego.InlineKeyboardMarkup {
	approve := handlers.CallbackData(handlers.ActionApprove, correlationID)
	deny := handlers.CallbackData(handlers.ActionDeny, correlationID)
	denyMsg := handlers.CallbackData(handlers.ActionDenyWithMessage, correlationID)
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(s.messages.ApproveButton).WithCallbackData(approve),
			tu.InlineKeyboardButton(s.messages.DenyButton).WithCallbackData(deny),
		),
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(s.messages.DenyWithMessageButton).WithCallbackData(denyMsg),
		),
	)
}

func (s *Service) updateMessage(ctx context.Context, approval *approvals.Approval, result approvals.Result, timeoutMessage string) {
	note := s.noteForResult(result, timeoutMessage)
	if strings.TrimSpace(note) == "" {
		return
	}
	newText := fmt.Sprintf("%s\n\n%s", approval.MessageText, note)
	if err := s.editMessage(ctx, approval.MessageID, newText); err != nil {
		s.log.Error("Failed to update telegram message", "error", err)
	}
}

func (s *Service) noteForResult(result approvals.Result, timeoutMessage string) string {
	switch result.Decision {
	case approvals.DecisionApprove:
		return s.messages.ApprovedNote
	case approvals.DecisionDeny:
		return s.messages.DeniedNote
	case approvals.DecisionError:
		if result.Reason == timeoutReason {
			if strings.TrimSpace(timeoutMessage) != "" {
				return timeoutMessage
			}
			return s.messages.TimeoutNote
		}
		if strings.TrimSpace(result.Reason) != "" {
			return fmt.Sprintf("⚠️ %s", result.Reason)
		}
		return s.messages.ErrorNote
	default:
		return ""
	}
}

func (s *Service) editMessage(ctx context.Context, messageID int, text string) error {
	_, err := s.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      tu.ID(s.chatID),
		MessageID:   messageID,
		Text:        text,
		ParseMode:   telego.ModeMarkdown,
		ReplyMarkup: emptyKeyboard(),
	})
	return err
}

func emptyKeyboard() *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}}
}
