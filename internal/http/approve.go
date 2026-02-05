package http

import (
	"encoding/json"
	"fmt"
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
	CorrelationID   string              `json:"correlation_id"`
	Tool            string              `json:"tool"`
	Arguments       map[string]any      `json:"arguments"`
	Justification   string              `json:"justification,omitempty"`
	ApprovalRequest string              `json:"approval_request,omitempty"`
	LinksToCode     []approvals.Link    `json:"links_to_code,omitempty"`
	Lang            string              `json:"lang,omitempty"`
	Markup          string              `json:"markup,omitempty"`
	Callback        *approvals.Callback `json:"callback,omitempty"`
	TimeoutSec      int                 `json:"timeout_sec,omitempty"`
}

// ApproveResponse defines output payload for /approve.
type ApproveResponse struct {
	Decision      string `json:"decision"`
	Reason        string `json:"reason,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
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
	if req.Justification != "" {
		if err := validateReasonLength("justification", req.Justification); err != nil {
			h.respond(w, http.StatusBadRequest, approvals.DecisionError, err.Error())
			return
		}
	}
	if req.ApprovalRequest != "" {
		if err := validateReasonLength("approval_request", req.ApprovalRequest); err != nil {
			h.respond(w, http.StatusBadRequest, approvals.DecisionError, err.Error())
			return
		}
	}
	if len(req.LinksToCode) > 5 {
		req.LinksToCode = req.LinksToCode[:5]
	}
	for _, link := range req.LinksToCode {
		if strings.TrimSpace(link.Text) == "" || strings.TrimSpace(link.URL) == "" {
			h.respond(w, http.StatusBadRequest, approvals.DecisionError, "links_to_code items must include text and url")
			return
		}
	}
	if strings.TrimSpace(req.Markup) == "" {
		req.Markup = "markdown"
	}
	switch strings.ToLower(strings.TrimSpace(req.Markup)) {
	case "markdown", "html":
	default:
		h.respond(w, http.StatusBadRequest, approvals.DecisionError, "markup must be markdown or html")
		return
	}
	if strings.TrimSpace(req.Lang) == "" {
		req.Lang = h.cfg.Lang
	}
	if req.Callback == nil || strings.TrimSpace(req.Callback.URL) == "" {
		h.respond(w, http.StatusBadRequest, approvals.DecisionError, "callback.url is required for async approval")
		return
	}

	timeout := h.cfg.ApprovalTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}

	ctx := r.Context()
	res, err := h.svc.SubmitApproval(ctx, approvals.Request{
		CorrelationID:   req.CorrelationID,
		Tool:            req.Tool,
		Arguments:       req.Arguments,
		Justification:   req.Justification,
		ApprovalRequest: req.ApprovalRequest,
		LinksToCode:     req.LinksToCode,
		Lang:            req.Lang,
		Markup:          req.Markup,
		Callback:        *req.Callback,
	}, timeout, h.cfg.TimeoutMessage)
	if err != nil {
		h.log.Error("Approval request failed", "error", err)
		if res.Decision == "" {
			h.respond(w, http.StatusInternalServerError, approvals.DecisionError, "approval failed")
			return
		}
	}

	h.respond(w, http.StatusAccepted, res.Decision, res.Reason, req.CorrelationID)
}

func (h *ApproveHandler) respond(w http.ResponseWriter, status int, decision approvals.Decision, reason string, correlationID ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := ApproveResponse{Decision: string(decision), Reason: reason}
	if len(correlationID) > 0 {
		resp.CorrelationID = correlationID[0]
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		return
	}
}

func validateReasonLength(field, value string) error {
	length := len([]rune(strings.TrimSpace(value)))
	if length < 10 || length > 500 {
		return fmt.Errorf("%s must be 10-500 characters", field)
	}
	return nil
}
