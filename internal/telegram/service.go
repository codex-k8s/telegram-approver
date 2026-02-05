package telegram

import (
	"context"
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
	msg := s.messagesFor(req.Lang)
	switch strings.ToLower(strings.TrimSpace(req.Markup)) {
	case "html":
		return renderHTML(msg, req)
	default:
		return renderMarkdown(msg, req)
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
		return telego.ModeMarkdownV2
	}
}

func renderMarkdown(msg i18n.Messages, req approvals.Request) string {
	builder := &strings.Builder{}
	builder.WriteString("*")
	builder.WriteString(escapeMarkdownV2(msg.ApprovalTitle))
	builder.WriteString("*\n\n")
	contextTitle := msg.SectionContext
	if strings.TrimSpace(contextTitle) == "" {
		contextTitle = "Context"
	}
	actionTitle := msg.SectionAction
	if strings.TrimSpace(actionTitle) == "" {
		actionTitle = "Action"
	}
	risksTitle := msg.SectionRisks
	if strings.TrimSpace(risksTitle) == "" {
		risksTitle = "Risks"
	}

	builder.WriteString("*")
	builder.WriteString(escapeMarkdownV2(contextTitle))
	builder.WriteString("*\n")
	if strings.TrimSpace(req.ApprovalRequest) != "" {
		builder.WriteString(escapeMarkdownV2(req.ApprovalRequest))
		builder.WriteString("\n\n")
	}
	if strings.TrimSpace(req.Justification) != "" {
		label := msg.JustificationLabel
		if strings.TrimSpace(label) == "" {
			label = "Justification"
		}
		builder.WriteString("*")
		builder.WriteString(escapeMarkdownV2(label))
		builder.WriteString(":* ")
		builder.WriteString(escapeMarkdownV2(req.Justification))
		builder.WriteString("\n\n")
	}
	if len(req.LinksToCode) > 0 {
		label := msg.LinksLabel
		if strings.TrimSpace(label) == "" {
			label = "Links"
		}
		builder.WriteString("*")
		builder.WriteString(escapeMarkdownV2(label))
		builder.WriteString(":*\n")
		for _, link := range req.LinksToCode {
			builder.WriteString("• [")
			builder.WriteString(escapeMarkdownV2(link.Text))
			builder.WriteString("](")
			builder.WriteString(escapeMarkdownV2URL(link.URL))
			builder.WriteString(")\n")
		}
		builder.WriteString("\n")
	}

	if strings.TrimSpace(req.RiskAssessment) != "" {
		builder.WriteString("*")
		builder.WriteString(escapeMarkdownV2(risksTitle))
		builder.WriteString("*\n")
		builder.WriteString(escapeMarkdownV2(req.RiskAssessment))
		builder.WriteString("\n\n")
	}

	builder.WriteString("*")
	builder.WriteString(escapeMarkdownV2(actionTitle))
	builder.WriteString("*\n")
	builder.WriteString("*")
	builder.WriteString(escapeMarkdownV2(msg.ApprovalTool))
	builder.WriteString(":* `")
	builder.WriteString(escapeMarkdownV2Code(req.Tool))
	builder.WriteString("`\n")
	builder.WriteString("*")
	builder.WriteString(escapeMarkdownV2(msg.ApprovalCorrelation))
	builder.WriteString(":* `")
	builder.WriteString(escapeMarkdownV2Code(req.CorrelationID))
	builder.WriteString("`\n\n")
	return builder.String()
}

func renderHTML(msg i18n.Messages, req approvals.Request) string {
	builder := &strings.Builder{}
	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(msg.ApprovalTitle))
	builder.WriteString("</b><br><br>")
	contextTitle := msg.SectionContext
	if strings.TrimSpace(contextTitle) == "" {
		contextTitle = "Context"
	}
	actionTitle := msg.SectionAction
	if strings.TrimSpace(actionTitle) == "" {
		actionTitle = "Action"
	}
	risksTitle := msg.SectionRisks
	if strings.TrimSpace(risksTitle) == "" {
		risksTitle = "Risks"
	}

	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(contextTitle))
	builder.WriteString("</b><br>")
	if strings.TrimSpace(req.ApprovalRequest) != "" {
		builder.WriteString(htmlEscape(req.ApprovalRequest))
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
		builder.WriteString(htmlEscape(req.Justification))
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

	if strings.TrimSpace(req.RiskAssessment) != "" {
		builder.WriteString("<b>")
		builder.WriteString(htmlEscape(risksTitle))
		builder.WriteString("</b><br>")
		builder.WriteString(htmlEscape(req.RiskAssessment))
		builder.WriteString("<br><br>")
	}

	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(actionTitle))
	builder.WriteString("</b><br>")
	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(msg.ApprovalTool))
	builder.WriteString(":</b> <code>")
	builder.WriteString(htmlEscape(req.Tool))
	builder.WriteString("</code><br>")
	builder.WriteString("<b>")
	builder.WriteString(htmlEscape(msg.ApprovalCorrelation))
	builder.WriteString(":</b> <code>")
	builder.WriteString(htmlEscape(req.CorrelationID))
	builder.WriteString("</code><br><br>")
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

func escapeMarkdownV2(value string) string {
	if value == "" {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value) * 2)
	for _, r := range value {
		switch r {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!', '\\':
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func escapeMarkdownV2Code(value string) string {
	if value == "" {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value) * 2)
	for _, r := range value {
		switch r {
		case '\\', '`':
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func escapeMarkdownV2URL(value string) string {
	if value == "" {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value) * 2)
	for _, r := range value {
		switch r {
		case '\\', ')':
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
