package approvals

import (
	"errors"
	"sync"
	"time"
)

// Decision describes an approval decision.
type Decision string

const (
	// DecisionApprove means the request is approved.
	DecisionApprove Decision = "approve"
	// DecisionDeny means the request is denied.
	DecisionDeny Decision = "deny"
	// DecisionError means the request failed.
	DecisionError Decision = "error"
	// DecisionPending means the request is queued for async approval.
	DecisionPending Decision = "pending"
)

// Link points to a code reference.
type Link struct {
	// Text is the link label.
	Text string `json:"text"`
	// URL is the target URL.
	URL string `json:"url"`
}

// Callback defines async approval callback settings.
type Callback struct {
	// URL is the webhook callback URL.
	URL string `json:"url"`
}

// Request holds data required for approval.
type Request struct {
	// CorrelationID links related requests.
	CorrelationID string
	// Tool is the tool name.
	Tool string
	// Arguments are tool arguments.
	Arguments map[string]any
	// Justification is a short reason from the model.
	Justification string
	// ApprovalRequest describes the requested action.
	ApprovalRequest string
	// LinksToCode are optional references.
	LinksToCode []Link
	// Lang selects message language.
	Lang string
	// Markup selects message formatting.
	Markup string
	// Callback contains webhook details.
	Callback Callback
}

// Result represents the approval result.
type Result struct {
	// Decision is the approval decision.
	Decision Decision
	// Reason contains human-readable details.
	Reason string
}

// Approval stores state for a single approval request.
type Approval struct {
	// Request is the approval request payload.
	Request Request
	// CreatedAt is the request creation time.
	CreatedAt time.Time
	// MessageID is the Telegram message ID.
	MessageID int
	// MessageText is the Telegram message text.
	MessageText string
	// AwaitingReason marks that a deny reason is pending.
	AwaitingReason bool
}

// Registry stores active approval requests.
type Registry struct {
	mu                sync.Mutex
	approvals         map[string]*Approval
	promptMessageID   int
	promptCorrelation string
}

// ErrAlreadyExists is returned when the correlation id is already used.
var ErrAlreadyExists = errors.New("approval already exists")

// NewRegistry creates a new approval registry.
func NewRegistry() *Registry {
	return &Registry{approvals: make(map[string]*Approval)}
}

// Add registers a new approval request.
func (r *Registry) Add(req Request) (*Approval, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.approvals[req.CorrelationID]; exists {
		return nil, ErrAlreadyExists
	}
	approval := &Approval{
		Request:   req,
		CreatedAt: time.Now(),
	}
	r.approvals[req.CorrelationID] = approval
	return approval, nil
}

// Get returns the approval by correlation id.
func (r *Registry) Get(correlationID string) *Approval {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.approvals[correlationID]
}

// SetMessage stores Telegram message metadata for the approval.
func (r *Registry) SetMessage(correlationID string, messageID int, messageText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if approval, ok := r.approvals[correlationID]; ok {
		approval.MessageID = messageID
		approval.MessageText = messageText
	}
}

// StartReason marks approval as waiting for a deny reason and returns prompt to delete.
func (r *Registry) StartReason(correlationID string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	approval, ok := r.approvals[correlationID]
	if !ok {
		return 0, false
	}
	var previousPrompt int
	if r.promptCorrelation != "" && r.promptCorrelation != correlationID {
		if prevApproval, exists := r.approvals[r.promptCorrelation]; exists {
			prevApproval.AwaitingReason = false
		}
		previousPrompt = r.promptMessageID
	}
	approval.AwaitingReason = true
	r.promptCorrelation = correlationID
	r.promptMessageID = 0
	return previousPrompt, true
}

// SetPromptMessage stores the prompt message ID for the current deny flow.
func (r *Registry) SetPromptMessage(correlationID string, messageID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.promptCorrelation == correlationID {
		r.promptMessageID = messageID
	}
}

// ClearPrompt removes the active deny prompt if it matches correlationID.
func (r *Registry) ClearPrompt(correlationID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.promptCorrelation != correlationID {
		return 0
	}
	if approval, ok := r.approvals[correlationID]; ok {
		approval.AwaitingReason = false
	}
	removed := r.promptMessageID
	r.promptMessageID = 0
	r.promptCorrelation = ""
	return removed
}

// CurrentPrompt returns the approval awaiting a deny reason and its prompt message id.
func (r *Registry) CurrentPrompt() (*Approval, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.promptCorrelation == "" {
		return nil, 0
	}
	approval := r.approvals[r.promptCorrelation]
	if approval == nil || !approval.AwaitingReason {
		return nil, 0
	}
	return approval, r.promptMessageID
}

// Resolve removes the approval from the registry and clears prompt if needed.
func (r *Registry) Resolve(correlationID string) (*Approval, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	approval, ok := r.approvals[correlationID]
	if !ok {
		return nil, 0, false
	}
	delete(r.approvals, correlationID)
	promptID := 0
	if r.promptCorrelation == correlationID {
		promptID = r.promptMessageID
		r.promptMessageID = 0
		r.promptCorrelation = ""
	}
	return approval, promptID, true
}
