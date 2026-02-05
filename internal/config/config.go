package config

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config describes runtime configuration for telegram-approver.
type Config struct {
	// ServiceName is a human-friendly service name for logs.
	ServiceName string `env:"TG_APPROVER_SERVICE_NAME" envDefault:"telegram-approver"`
	// HTTPHost is the HTTP listen host.
	HTTPHost string `env:"TG_APPROVER_HTTP_HOST,required"`
	// HTTPPort is the HTTP listen port.
	HTTPPort int `env:"TG_APPROVER_HTTP_PORT" envDefault:"8080"`
	// LogLevel controls log verbosity (debug, info, warn, error).
	LogLevel string `env:"TG_APPROVER_LOG_LEVEL" envDefault:"info"`
	// Lang selects i18n language (en or ru).
	Lang string `env:"TG_APPROVER_LANG" envDefault:"en"`
	// Token is the Telegram bot token.
	Token string `env:"TG_APPROVER_TOKEN,required"`
	// ChatID is the allowed Telegram chat ID.
	ChatID int64 `env:"TG_APPROVER_CHAT_ID,required"`
	// ApprovalTimeout is the maximum time to wait for user decision.
	ApprovalTimeout time.Duration `env:"TG_APPROVER_APPROVAL_TIMEOUT" envDefault:"1h"`
	// TimeoutMessage overrides the timeout message appended to Telegram messages.
	TimeoutMessage string `env:"TG_APPROVER_TIMEOUT_MESSAGE"`
	// WebhookURL enables webhook mode when set with WebhookSecret.
	WebhookURL string `env:"TG_APPROVER_WEBHOOK_URL"`
	// WebhookSecret is the Telegram webhook secret token.
	WebhookSecret string `env:"TG_APPROVER_WEBHOOK_SECRET"`
	// OpenAIAPIKey enables voice transcription.
	OpenAIAPIKey string `env:"TG_APPROVER_OPENAI_API_KEY"`
	// STTModel is the OpenAI model for transcription.
	STTModel string `env:"TG_APPROVER_STT_MODEL" envDefault:"gpt-4o-mini-transcribe"`
	// STTTimeout is the OpenAI transcription timeout.
	STTTimeout time.Duration `env:"TG_APPROVER_STT_TIMEOUT" envDefault:"30s"`
	// ShutdownTimeout is the graceful shutdown timeout.
	ShutdownTimeout time.Duration `env:"TG_APPROVER_SHUTDOWN_TIMEOUT" envDefault:"10s"`
}

// Load parses configuration from environment variables.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, err
	}

	cfg.Lang = strings.ToLower(strings.TrimSpace(cfg.Lang))
	if cfg.Lang == "" {
		cfg.Lang = "en"
	}

	if cfg.ApprovalTimeout <= 0 {
		return Config{}, fmt.Errorf("approval timeout must be positive")
	}

	if strings.TrimSpace(cfg.HTTPHost) == "" {
		return Config{}, fmt.Errorf("http host is required")
	}
	if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		return Config{}, fmt.Errorf("http port must be between 1 and 65535")
	}

	if (cfg.WebhookURL == "") != (cfg.WebhookSecret == "") {
		return Config{}, fmt.Errorf("webhook url and secret must be set together")
	}

	return cfg, nil
}

// HTTPAddr returns a listen address for the HTTP server.
func (c Config) HTTPAddr() string {
	return net.JoinHostPort(strings.TrimSpace(c.HTTPHost), fmt.Sprintf("%d", c.HTTPPort))
}

// WebhookEnabled reports whether webhook mode is configured.
func (c Config) WebhookEnabled() bool {
	return c.WebhookURL != "" && c.WebhookSecret != ""
}
