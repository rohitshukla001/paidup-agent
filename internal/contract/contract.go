// Package contract holds the interfaces between PaidUp packages.
//
// Task 1 owns this package. Every other task codes against these interfaces
// and never against another task's package. To change an interface, update
// Section 4 of docs/implementation-plan.md first, then this file, then tell
// each task that uses it.
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"paidup-agent/internal/domain"
)

// Shared errors. Implementations wrap them, so callers can test with errors.Is.
var (
	ErrNotFound    = errors.New("not found")
	ErrValidation  = domain.ErrValidation
	ErrRateLimited = errors.New("rate limited")
	ErrUpstream    = errors.New("upstream error")
	// ErrBusy means every Gemini model returned 429 or the global limit was hit.
	ErrBusy = errors.New("llm: all models are rate limited")
)

// LLM is the Gemini client. Task 3 implements it.
type LLM interface {
	// Chat sends one request and returns text, function calls, or both.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// ExtractInvoice reads text and an optional image and returns a draft.
	// The draft is not validated; call domain.ValidateDraft.
	ExtractInvoice(ctx context.Context, text string, image []byte, mimeType string) (domain.InvoiceDraft, error)
}

// ToolDecl declares one function the model can call.
type ToolDecl struct {
	Name        string
	Description string
	Params      map[string]any // JSON schema of the arguments
}

// FuncCall is one function call that the model asked for.
type FuncCall struct {
	Name string
	Args map[string]any
	// Signature is the Gemini thought signature. Send it back unchanged in the
	// next request, or multi-step tool calls can fail.
	Signature []byte
}

// FuncResult is the output of one function call, sent back to the model.
type FuncResult struct {
	Name   string
	Output map[string]any
}

// Turn is one entry of chat history. Role is "user" or "model".
// A user turn carries Text, an optional image, or Results.
// A model turn carries Text, Calls, or both.
type Turn struct {
	Role     string
	Text     string
	Image    []byte
	MIMEType string
	Calls    []FuncCall
	Results  []FuncResult
}

// ChatRequest is one call to the model.
type ChatRequest struct {
	System  string
	History []Turn
	Tools   []ToolDecl
}

// ChatResponse is the model reply.
type ChatResponse struct {
	Text  string
	Calls []FuncCall
	Model string // the model that answered, after any fallback
}

// PayPal is the PayPal REST client. Task 2 implements it.
// Every write call takes a requestID that goes in the PayPal-Request-Id header.
type PayPal interface {
	CreateDraft(ctx context.Context, d domain.InvoiceDraft, requestID string) (domain.Invoice, error)
	Send(ctx context.Context, invoiceID, requestID string) error
	Remind(ctx context.Context, invoiceID, subject, note, requestID string) error
	Cancel(ctx context.Context, invoiceID, reason, requestID string) error
	Get(ctx context.Context, invoiceID string) (domain.Invoice, error)
	// List returns invoices with the given PayPal status, or all if status is "".
	List(ctx context.Context, status string, limit int) ([]domain.Invoice, error)
	Search(ctx context.Context, email string, from, to time.Time) ([]domain.Invoice, error)
	// VerifyWebhook checks a webhook signature.
	// rawBody must be the bytes exactly as received.
	VerifyWebhook(ctx context.Context, h http.Header, rawBody []byte) (bool, error)
}

// Store is the database. Task 5 implements it.
type Store interface {
	CreateSession(ctx context.Context) (string, error)
	CountAIRequests(ctx context.Context, sessionID string, since time.Time) (int, error)
	RecordAIRequest(ctx context.Context, sessionID string) error

	AddMessage(ctx context.Context, m domain.Message) error
	// RecentMessages returns up to limit messages, oldest first.
	RecentMessages(ctx context.Context, sessionID string, limit int) ([]domain.Message, error)

	SaveAction(ctx context.Context, a domain.PendingAction) error
	GetAction(ctx context.Context, id string) (domain.PendingAction, error)
	// ListActions returns actions with the given status, or all if status is "".
	ListActions(ctx context.Context, status domain.ActionStatus) ([]domain.PendingAction, error)
	// TransitionAction changes the status only if it is "from". It is atomic.
	// It returns false when the status was not "from".
	TransitionAction(ctx context.Context, id string, from, to domain.ActionStatus) (bool, error)
	SetActionResult(ctx context.Context, id string, to domain.ActionStatus, result json.RawMessage) error
	HasPendingAction(ctx context.Context, tool, invoiceID string) (bool, error)

	UpsertInvoice(ctx context.Context, inv domain.Invoice) error
	// ListInvoices returns cached invoices with the given status.
	// An empty status returns all invoices.
	ListInvoices(ctx context.Context, status string) ([]domain.Invoice, error)

	// SaveEventOnce returns false if the event ID was saved before.
	SaveEventOnce(ctx context.Context, eventID, eventType string, payload []byte) (bool, error)

	AddReminder(ctx context.Context, invoiceID, note string) error
	LastReminder(ctx context.Context, invoiceID string) (time.Time, bool, error)

	// Reset deletes all demo data.
	Reset(ctx context.Context) error
}

// Agent runs the chat loop and the approval flow. Task 4 implements it.
type Agent interface {
	HandleMessage(ctx context.Context, sessionID, text string, image []byte, mimeType string, emit func(domain.Event)) error
	Approve(ctx context.Context, sessionID, actionID string, emit func(domain.Event)) error
	Reject(ctx context.Context, sessionID, actionID string) error
}

// Bus fans out events to live dashboard clients. Task 6 implements it.
type Bus interface {
	Publish(e domain.Event)
	// Subscribe returns a channel of events and a function that ends
	// the subscription.
	Subscribe() (<-chan domain.Event, func())
}
