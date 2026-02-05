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
	"github.com/codex-k8s/telegram-approver/internal/telegram/shared"
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
	return shared.MessagesFor(s.messages, lang, s.lang)
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
	return renderApproval(msg, req, markdownApprovalWriter{})
}

func renderHTML(msg i18n.Messages, req approvals.Request) string {
	return renderApproval(msg, req, htmlApprovalWriter{})
}

func renderApproval(msg i18n.Messages, req approvals.Request, writer approvalMessageWriter) string {
	labels := approvalLabelsFor(msg)
	builder := &strings.Builder{}
	writer.WriteTitle(builder, msg.ApprovalTitle)

	writer.WriteSectionHeader(builder, labels.ContextTitle)
	if strings.TrimSpace(req.ApprovalRequest) != "" {
		writer.WritePlain(builder, req.ApprovalRequest, true)
	}
	if strings.TrimSpace(req.Justification) != "" {
		writer.WriteLabelValue(builder, labels.JustificationLabel, req.Justification, true)
	}
	if len(req.LinksToCode) > 0 {
		writer.WriteLinks(builder, labels.LinksLabel, req.LinksToCode)
	}
	if strings.TrimSpace(req.RiskAssessment) != "" {
		writer.WriteSectionHeader(builder, labels.RisksTitle)
		writer.WritePlain(builder, req.RiskAssessment, true)
	}
	writer.WriteSectionHeader(builder, labels.ActionTitle)
	writer.WriteCodeValue(builder, msg.ApprovalTool, req.Tool, false)
	writer.WriteCodeValue(builder, msg.ApprovalCorrelation, req.CorrelationID, true)
	return builder.String()
}

type approvalMessageWriter interface {
	WriteTitle(builder *strings.Builder, title string)
	WriteSectionHeader(builder *strings.Builder, title string)
	WritePlain(builder *strings.Builder, value string, addEmptyLine bool)
	WriteLabelValue(builder *strings.Builder, label, value string, addEmptyLine bool)
	WriteCodeValue(builder *strings.Builder, label, value string, addEmptyLine bool)
	WriteLinks(builder *strings.Builder, label string, links []approvals.Link)
}

type markdownApprovalWriter struct{}

func (markdownApprovalWriter) WriteTitle(builder *strings.Builder, title string) {
	builder.WriteString("*")
	builder.WriteString(shared.EscapeMarkdownV2(title))
	builder.WriteString("*\n\n")
}

func (markdownApprovalWriter) WriteSectionHeader(builder *strings.Builder, title string) {
	builder.WriteString("*")
	builder.WriteString(shared.EscapeMarkdownV2(title))
	builder.WriteString("*\n")
}

func (markdownApprovalWriter) WritePlain(builder *strings.Builder, value string, addEmptyLine bool) {
	builder.WriteString(shared.EscapeMarkdownV2(value))
	builder.WriteString("\n")
	appendOptionalLineBreak(builder, "\n", addEmptyLine)
}

func (markdownApprovalWriter) WriteLabelValue(builder *strings.Builder, label, value string, addEmptyLine bool) {
	builder.WriteString("*")
	builder.WriteString(shared.EscapeMarkdownV2(label))
	builder.WriteString(":* ")
	builder.WriteString(shared.EscapeMarkdownV2(value))
	builder.WriteString("\n")
	appendOptionalLineBreak(builder, "\n", addEmptyLine)
}

func (markdownApprovalWriter) WriteCodeValue(builder *strings.Builder, label, value string, addEmptyLine bool) {
	builder.WriteString("*")
	builder.WriteString(shared.EscapeMarkdownV2(label))
	builder.WriteString(":* `")
	builder.WriteString(shared.EscapeMarkdownV2Code(value))
	builder.WriteString("`\n")
	appendOptionalLineBreak(builder, "\n", addEmptyLine)
}

func (markdownApprovalWriter) WriteLinks(builder *strings.Builder, label string, links []approvals.Link) {
	builder.WriteString("*")
	builder.WriteString(shared.EscapeMarkdownV2(label))
	builder.WriteString(":*\n")
	for _, link := range links {
		builder.WriteString("• [")
		builder.WriteString(shared.EscapeMarkdownV2(link.Text))
		builder.WriteString("](")
		builder.WriteString(shared.EscapeMarkdownV2URL(link.URL))
		builder.WriteString(")\n")
	}
	builder.WriteString("\n")
}

type htmlApprovalWriter struct{}

func (htmlApprovalWriter) WriteTitle(builder *strings.Builder, title string) {
	builder.WriteString("<b>")
	builder.WriteString(shared.EscapeHTML(title))
	builder.WriteString("</b><br><br>")
}

func (htmlApprovalWriter) WriteSectionHeader(builder *strings.Builder, title string) {
	builder.WriteString("<b>")
	builder.WriteString(shared.EscapeHTML(title))
	builder.WriteString("</b><br>")
}

func (htmlApprovalWriter) WritePlain(builder *strings.Builder, value string, addEmptyLine bool) {
	builder.WriteString(shared.EscapeHTML(value))
	builder.WriteString("<br>")
	appendOptionalLineBreak(builder, "<br>", addEmptyLine)
}

func (htmlApprovalWriter) WriteLabelValue(builder *strings.Builder, label, value string, addEmptyLine bool) {
	builder.WriteString("<b>")
	builder.WriteString(shared.EscapeHTML(label))
	builder.WriteString(":</b> ")
	builder.WriteString(shared.EscapeHTML(value))
	builder.WriteString("<br>")
	appendOptionalLineBreak(builder, "<br>", addEmptyLine)
}

func (htmlApprovalWriter) WriteCodeValue(builder *strings.Builder, label, value string, addEmptyLine bool) {
	builder.WriteString("<b>")
	builder.WriteString(shared.EscapeHTML(label))
	builder.WriteString(":</b> <code>")
	builder.WriteString(shared.EscapeHTML(value))
	builder.WriteString("</code><br>")
	appendOptionalLineBreak(builder, "<br>", addEmptyLine)
}

func (htmlApprovalWriter) WriteLinks(builder *strings.Builder, label string, links []approvals.Link) {
	builder.WriteString("<b>")
	builder.WriteString(shared.EscapeHTML(label))
	builder.WriteString(":</b><br>")
	for _, link := range links {
		builder.WriteString("• <a href=\"")
		builder.WriteString(shared.EscapeHTML(link.URL))
		builder.WriteString("\">")
		builder.WriteString(shared.EscapeHTML(link.Text))
		builder.WriteString("</a><br>")
	}
	builder.WriteString("<br>")
}

func appendOptionalLineBreak(builder *strings.Builder, lineBreak string, enabled bool) {
	if enabled {
		builder.WriteString(lineBreak)
	}
}

type approvalLabels struct {
	ContextTitle       string
	ActionTitle        string
	RisksTitle         string
	JustificationLabel string
	LinksLabel         string
}

func approvalLabelsFor(msg i18n.Messages) approvalLabels {
	return approvalLabels{
		ContextTitle:       fallbackText(msg.SectionContext, "Context"),
		ActionTitle:        fallbackText(msg.SectionAction, "Action"),
		RisksTitle:         fallbackText(msg.SectionRisks, "Risks"),
		JustificationLabel: fallbackText(msg.JustificationLabel, "Justification"),
		LinksLabel:         fallbackText(msg.LinksLabel, "Links"),
	}
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
