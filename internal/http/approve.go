package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/codex-k8s/telegram-approver/internal/approvals"
	"github.com/codex-k8s/telegram-approver/internal/config"
	"github.com/codex-k8s/telegram-approver/internal/telegram"
)

// ApproveHandler handles approval requests from yaml-mcp-server.
type ApproveHandler struct {
	svc *telegram.Service
	cfg config.Config
	log *slog.Logger
}

// NewApproveHandler creates a new approval handler.
func NewApproveHandler(svc *telegram.Service, cfg config.Config, log *slog.Logger) *ApproveHandler {
	return &ApproveHandler{svc: svc, cfg: cfg, log: log}
}

// ApproveRequest defines input payload for /approve.
type ApproveRequest struct {
	CorrelationID string         `json:"correlation_id"`
	Tool          string         `json:"tool"`
	Arguments     map[string]any `json:"arguments"`
	TimeoutSec    int            `json:"timeout_sec,omitempty"`
}

// ApproveResponse defines output payload for /approve.
type ApproveResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// ServeHTTP handles /approve requests.
func (h *ApproveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ApproveRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		h.respond(w, http.StatusBadRequest, approvals.DecisionError, "invalid json payload")
		return
	}
	if strings.TrimSpace(req.CorrelationID) == "" {
		h.respond(w, http.StatusBadRequest, approvals.DecisionError, "correlation_id is required")
		return
	}
	if strings.TrimSpace(req.Tool) == "" {
		h.respond(w, http.StatusBadRequest, approvals.DecisionError, "tool is required")
		return
	}
	if req.Arguments == nil {
		req.Arguments = map[string]any{}
	}

	timeout := h.cfg.ApprovalTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}

	ctx := r.Context()
	res, err := h.svc.RequestApproval(ctx, approvals.Request{
		CorrelationID: req.CorrelationID,
		Tool:          req.Tool,
		Arguments:     req.Arguments,
	}, timeout, h.cfg.TimeoutMessage)
	if err != nil {
		h.log.Error("Approval request failed", "error", err)
		if res.Decision == "" {
			h.respond(w, http.StatusInternalServerError, approvals.DecisionError, "approval failed")
			return
		}
	}

	h.respond(w, http.StatusOK, res.Decision, res.Reason)
}

func (h *ApproveHandler) respond(w http.ResponseWriter, status int, decision approvals.Decision, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := ApproveResponse{Decision: string(decision), Reason: reason}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		return
	}
}
