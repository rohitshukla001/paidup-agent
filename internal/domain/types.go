// Package domain holds the types and rules that every PaidUp package shares.
//
// Task 1 owns this package. Change it only after you change Section 4 of
// docs/implementation-plan.md.
package domain

import (
	"encoding/json"
	"time"
)

// LineItem is one billable line on an invoice.
type LineItem struct {
	Name      string  `json:"name"`
	Quantity  float64 `json:"quantity"`
	UnitPrice string  `json:"unit_price"` // decimal string, for example "450.00"
}

// InvoiceDraft is what Gemini extracts and what the user approves.
type InvoiceDraft struct {
	ClientName  string     `json:"client_name"`
	ClientEmail string     `json:"client_email"`
	Currency    string     `json:"currency"` // one of AllowedCurrencies
	Items       []LineItem `json:"items"`
	DueDays     int        `json:"due_days"`
	Note        string     `json:"note"`
}

// Invoice is the cached view of a PayPal invoice.
type Invoice struct {
	ID           string    `json:"id"`
	Number       string    `json:"number"`
	Status       string    `json:"status"` // PayPal status, see Bucket
	ClientName   string    `json:"client_name"`
	ClientEmail  string    `json:"client_email"`
	Currency     string    `json:"currency"`
	Total        string    `json:"total"`
	DueDate      time.Time `json:"due_date"`
	PayerViewURL string    `json:"payer_view_url"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ActionStatus is the lifecycle state of a PendingAction.
type ActionStatus string

// Action statuses.
const (
	ActionPending  ActionStatus = "PENDING"
	ActionApproved ActionStatus = "APPROVED"
	ActionRejected ActionStatus = "REJECTED"
	ActionExpired  ActionStatus = "EXPIRED"
	ActionDone     ActionStatus = "DONE"
	ActionFailed   ActionStatus = "FAILED"
)

// Write tool names. Read tools live in the agent package.
const (
	ToolProposeInvoice  = "propose_invoice"
	ToolProposeReminder = "propose_reminder"
	ToolProposeCancel   = "propose_cancel_invoice"
)

// Expiry windows for pending actions.
const (
	ChatActionTTL = 15 * time.Minute
	JobActionTTL  = 72 * time.Hour
)

// PendingAction is a write action that waits for the user to approve it.
type PendingAction struct {
	ID string `json:"id"`
	// SessionID is "" for actions that the reminder job creates.
	SessionID string          `json:"session_id"`
	Tool      string          `json:"tool"`
	InvoiceID string          `json:"invoice_id"` // "" for propose_invoice
	Args      json.RawMessage `json:"args"`
	Preview   json.RawMessage `json:"preview"`
	Status    ActionStatus    `json:"status"`
	// RequestID is a new UUID, set by the code that saves the action.
	RequestID string          `json:"request_id"`
	Result    json.RawMessage `json:"result,omitempty"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// Expired reports whether the action can no longer be approved.
func (a PendingAction) Expired(now time.Time) bool { return !now.Before(a.ExpiresAt) }

// IdempotencyKey returns the PayPal-Request-Id for one call inside an approval.
// step is "create", "send", "remind", or "cancel".
func (a PendingAction) IdempotencyKey(step string) string { return a.RequestID + "-" + step }

// Message is one turn of chat history.
type Message struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"` // user, model, tool
	// Content is text, or JSON for a function call or result.
	Content string `json:"content"`
	// ImageRef is the SHA-256 of the image. The server keeps no image bytes.
	ImageRef  string    `json:"image_ref"`
	CreatedAt time.Time `json:"created_at"`
}

// Event is one Server-Sent Event.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Event types.
const (
	EventMessageDelta   = "message.delta"
	EventMessageDone    = "message.done"
	EventActionProposed = "action.proposed"
	EventActionResult   = "action.result"
	EventInvoiceUpdated = "invoice.updated"
	EventError          = "error"
)

// Error codes sent in error events and error replies.
const (
	CodeRateLimited  = "rate_limited"
	CodeBusy         = "busy"
	CodeValidation   = "validation"
	CodeExpired      = "expired"
	CodeNotFound     = "not_found"
	CodeUnauthorized = "unauthorized"
	CodePayPalError  = "paypal_error"
	CodeInternal     = "internal"
)

// NewEvent builds an Event and encodes data as JSON.
func NewEvent(eventType string, data any) (Event, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	return Event{Type: eventType, Data: b}, nil
}

// ErrorData is the payload of an error event and of an error reply.
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Summary holds the totals for one currency.
type Summary struct {
	Currency string `json:"currency"`
	Paid     string `json:"paid"`
	Unpaid   string `json:"unpaid"`
	Overdue  string `json:"overdue"`
}
