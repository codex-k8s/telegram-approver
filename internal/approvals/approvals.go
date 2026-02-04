package approvals

import (
	"context"
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
)

// Request holds data required for approval.
type Request struct {
	// CorrelationID links related requests.
	CorrelationID string
	// Tool is the tool name.
	Tool string
	// Arguments are tool arguments.
	Arguments map[string]any
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
	resultCh       chan Result
	resolved       bool
}

// Registry stores the active approval request.
type Registry struct {
	slot   chan struct{}
	mu     sync.Mutex
	active *Approval
}

// ErrBusy is returned when an approval is already active.
var ErrBusy = errors.New("approval already active")

// NewRegistry creates a new approval registry.
func NewRegistry() *Registry {
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	return &Registry{slot: slot}
}

// Acquire blocks until the registry is ready to accept a new request.
func (r *Registry) Acquire(ctx context.Context) error {
	select {
	case <-r.slot:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees the registry for a new request.
func (r *Registry) Release() {
	r.slot <- struct{}{}
}

// Start registers a new approval request.
func (r *Registry) Start(req Request) (*Approval, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return nil, ErrBusy
	}
	approval := &Approval{
		Request:   req,
		CreatedAt: time.Now(),
		resultCh:  make(chan Result, 1),
	}
	r.active = approval
	return approval, nil
}

// Active returns the currently active approval.
func (r *Registry) Active() *Approval {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// SetMessage stores Telegram message metadata for the active approval.
func (r *Registry) SetMessage(approval *Approval, messageID int, messageText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == approval {
		approval.MessageID = messageID
		approval.MessageText = messageText
	}
}

// MarkAwaitingReason switches the active approval into waiting for deny reason.
func (r *Registry) MarkAwaitingReason(approval *Approval) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil || r.active != approval || approval.resolved {
		return false
	}
	approval.AwaitingReason = true
	return true
}

// Resolve finalizes the active approval with the provided decision.
func (r *Registry) Resolve(approval *Approval, decision Decision, reason string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil || r.active != approval || approval.resolved {
		return false
	}
	approval.resolved = true
	approval.AwaitingReason = false
	approval.resultCh <- Result{Decision: decision, Reason: reason}
	return true
}

// Clear removes the active approval state.
func (r *Registry) Clear(approval *Approval) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == approval {
		r.active = nil
	}
}

// Wait blocks until a result, timeout, or context cancellation.
func (r *Registry) Wait(ctx context.Context, approval *Approval, timeout time.Duration, timeoutReason string) Result {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-approval.resultCh:
		return res
	case <-timer.C:
		if !r.Resolve(approval, DecisionError, timeoutReason) {
			select {
			case res := <-approval.resultCh:
				return res
			default:
			}
		}
		return Result{Decision: DecisionError, Reason: timeoutReason}
	case <-ctx.Done():
		reason := "request cancelled"
		if !r.Resolve(approval, DecisionError, reason) {
			select {
			case res := <-approval.resultCh:
				return res
			default:
			}
		}
		return Result{Decision: DecisionError, Reason: reason}
	}
}
