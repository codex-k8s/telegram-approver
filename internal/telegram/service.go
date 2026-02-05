package telegram

import (
	"context"
	"encoding/json"
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
	messages map[string]i18n.Messages
	lang     string
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

	messages := map[string]i18n.Messages{
		bundle.Lang: bundle.Messages,
	}
	if bundle.Lang != "en" {
		if extra, err := i18n.Load("en"); err == nil {
			messages[extra.Lang] = extra.Messages
		}
	}
	if bundle.Lang != "ru" {
		if extra, err := i18n.Load("ru"); err == nil {
			messages[extra.Lang] = extra.Messages
		}
	}

	handler := handlers.NewHandler(bot, registry, messages, cfg.Lang, cfg.ChatID, sttLang, transcriber, log)

	return &Service{
		bot:      bot,
		source:   source,
		handler:  handler,
		registry: registry,
		log:      log,
		messages: messages,
		lang:     cfg.Lang,
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

// SubmitApproval sends approval request to Telegram and returns immediately.
func (s *Service) SubmitApproval(ctx context.Context, req approvals.Request, timeout time.Duration, timeoutMessage string) (approvals.Result, error) {
	if timeout <= 0 {
		timeout = time.Hour
	}
	_, err := s.registry.Add(req)
	if err != nil {
		return approvals.Result{Decision: approvals.DecisionError, Reason: "approval already exists"}, nil
	}

	messageText := s.renderMessage(req)
	keyboard := s.approvalKeyboard(req.CorrelationID, req.Lang)
	parseMode := parseMode(req.Markup)

	msg, err := s.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      tu.ID(s.chatID),
		Text:        messageText,
		ParseMode:   parseMode,
		ReplyMarkup: keyboard,
	})
	if err != nil {
		s.log.Error("Failed to send telegram message", "error", err)
		return approvals.Result{Decision: approvals.DecisionError, Reason: "failed to send telegram message"}, err
	}

	s.registry.SetMessage(req.CorrelationID, msg.MessageID, messageText)
	s.scheduleTimeout(req.CorrelationID, timeout, timeoutMessage)
	return approvals.Result{Decision: approvals.DecisionPending, Reason: "queued"}, nil
}

func (s *Service) renderMessage(req approvals.Request) string {
	payload, err := json.MarshalIndent(req.Arguments, "", "  ")
	if err != nil {
		payload = []byte("{}")
	}
	msg := s.messagesFor(req.Lang)
	switch strings.ToLower(strings.TrimSpace(req.Markup)) {
	case "html":
		return renderHTML(msg, req, payload)
	default:
		return renderMarkdown(msg, req, payload)
	}
}

func (s *Service) approvalKeyboard(correlationID, lang string) *telego.InlineKeyboardMarkup {
	msg := s.messagesFor(lang)
	approve := handlers.CallbackData(handlers.ActionApprove, correlationID)
	deny := handlers.CallbackData(handlers.ActionDeny, correlationID)
	denyMsg := handlers.CallbackData(handlers.ActionDenyWithMessage, correlationID)
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(msg.ApproveButton).WithCallbackData(approve),
			tu.InlineKeyboardButton(msg.DenyButton).WithCallbackData(deny),
		),
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(msg.DenyWithMessageButton).WithCallbackData(denyMsg),
		),
	)
}

func (s *Service) scheduleTimeout(correlationID string, timeout time.Duration, timeoutMessage string) {
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		<-timer.C
		approval, promptID, ok := s.registry.Resolve(correlationID)
		if !ok {
			return
		}
		if promptID > 0 {
			_ = s.handler.DeleteMessage(context.Background(), promptID)
		}
		s.handler.FinalizeApproval(context.Background(), approval, approvals.Result{
			Decision: approvals.DecisionError,
			Reason:   timeoutReason,
		}, timeoutMessage)
	}()
}

func (s *Service) messagesFor(lang string) i18n.Messages {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = s.lang
	}
	if msg, ok := s.messages[lang]; ok {
		return msg
	}
	if msg, ok := s.messages["en"]; ok {
		return msg
	}
	return i18n.Messages{}
}

func parseMode(markup string) string {
	switch strings.ToLower(strings.TrimSpace(markup)) {
	case "html":
		return telego.ModeHTML
	default:
		return telego.ModeMarkdown
	}
}

func renderMarkdown(msg i18n.Messages, req approvals.Request, payload []byte) string {
	builder := &strings.Builder{}
	builder.WriteString("*")
	builder.WriteString(msg.ApprovalTitle)
	builder.WriteString("*\n\n")
	builder.WriteString("*")
	builder.WriteString(msg.ApprovalCorrelation)
	builder.WriteString("*")
	builder.WriteString(": `")
	builder.WriteString(req.CorrelationID)
	builder.WriteString("`\n")
	builder.WriteString("*")
	builder.WriteString(msg.ApprovalTool)
	builder.WriteString("*")
	builder.WriteString(": `")
	builder.WriteString(req.Tool)
	builder.WriteString("`\n\n")
	if strings.TrimSpace(req.ApprovalRequest) != "" {
		builder.WriteString(req.ApprovalRequest)
		builder.WriteString("\n\n")
	}
	if strings.TrimSpace(req.Justification) != "" {
		label := msg.JustificationLabel
		if strings.TrimSpace(label) == "" {
			label = "Justification"
		}
		builder.WriteString("*")
		builder.WriteString(label)
		builder.WriteString(":* ")
		builder.WriteString(req.Justification)
		builder.WriteString("\n\n")
	}
	if len(req.LinksToCode) > 0 {
		label := msg.LinksLabel
		if strings.TrimSpace(label) == "" {
			label = "Links"
		}
		builder.WriteString("*")
		builder.WriteString(label)
		builder.WriteString(":*\n")
		for _, link := range req.LinksToCode {
			builder.WriteString("- [")
			builder.WriteString(link.Text)
			builder.WriteString("](")
			builder.WriteString(link.URL)
			builder.WriteString(")\n")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("*")
	builder.WriteString(msg.ApprovalParams)
	builder.WriteString("*")
	builder.WriteString(":\n\n```json\n")
	builder.Write(payload)
	builder.WriteString("\n```")
	return builder.String()
}

func renderHTML(msg i18n.Messages, req approvals.Request, payload []byte) string {
	builder := &strings.Builder{}
	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(msg.ApprovalTitle))
	builder.WriteString("</b><br><br>")
	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(msg.ApprovalCorrelation))
	builder.WriteString("</b>")
	builder.WriteString(": <code>")
	builder.WriteString(htmlEscape(req.CorrelationID))
	builder.WriteString("</code><br>")
	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(msg.ApprovalTool))
	builder.WriteString("</b>")
	builder.WriteString(": <code>")
	builder.WriteString(htmlEscape(req.Tool))
	builder.WriteString("</code><br><br>")
	if strings.TrimSpace(req.ApprovalRequest) != "" {
		builder.WriteString(req.ApprovalRequest)
		builder.WriteString("<br><br>")
	}
	if strings.TrimSpace(req.Justification) != "" {
		label := msg.JustificationLabel
		if strings.TrimSpace(label) == "" {
			label = "Justification"
		}
		builder.WriteString("<b>")
		builder.WriteString(htmlEscape(label))
		builder.WriteString(":</b> ")
		builder.WriteString(req.Justification)
		builder.WriteString("<br><br>")
	}
	if len(req.LinksToCode) > 0 {
		label := msg.LinksLabel
		if strings.TrimSpace(label) == "" {
			label = "Links"
		}
		builder.WriteString("<b>")
		builder.WriteString(htmlEscape(label))
		builder.WriteString(":</b><br>")
		for _, link := range req.LinksToCode {
			builder.WriteString("• <a href=\"")
			builder.WriteString(htmlEscape(link.URL))
			builder.WriteString("\">")
			builder.WriteString(htmlEscape(link.Text))
			builder.WriteString("</a><br>")
		}
		builder.WriteString("<br>")
	}
	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(msg.ApprovalParams))
	builder.WriteString("</b>")
	builder.WriteString(":<br><pre><code>")
	builder.WriteString(htmlEscape(string(payload)))
	builder.WriteString("</code></pre>")
	return builder.String()
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}
