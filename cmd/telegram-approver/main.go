package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/codex-k8s/telegram-approver/internal/approvals"
	"github.com/codex-k8s/telegram-approver/internal/config"
	httpapi "github.com/codex-k8s/telegram-approver/internal/http"
	"github.com/codex-k8s/telegram-approver/internal/i18n"
	"github.com/codex-k8s/telegram-approver/internal/log"
	"github.com/codex-k8s/telegram-approver/internal/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger := log.New(cfg.LogLevel)
	bundle, err := i18n.Load(cfg.Lang)
	if err != nil {
		logger.Error("failed to load i18n", "error", err)
		os.Exit(1)
	}

	registry := approvals.NewRegistry()
	service, err := telegram.New(cfg, bundle, registry, logger)
	if err != nil {
		logger.Error("failed to init telegram service", "error", err)
		os.Exit(1)
	}

	server := httpapi.New(cfg.HTTPAddr(), logger)
	server.Handle("/approve", httpapi.NewApproveHandler(service, cfg, logger))
	if webhook := service.WebhookHandler(); webhook != nil {
		server.Handle("/webhook", webhook)
	}

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(baseCtx); err != nil {
		logger.Error("failed to start telegram updates", "error", err)
		os.Exit(1)
	}
	server.SetReady(true)

	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)

	select {
	case sig := <-sigCh:
		logger.Info("shutdown requested", "signal", sig.String())
	case err := <-errCh:
		logger.Error("http server stopped", "error", err)
	}

	cancel()
	server.SetReady(false)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	_ = service.Stop(shutdownCtx)
}
