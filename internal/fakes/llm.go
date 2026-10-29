package fakes

import (
	"context"
	"sync"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// LLM is a scriptable fake of contract.LLM.
//
// Set ChatFunc or ExtractFunc to control the replies. With neither set, Chat
// echoes the last user text and ExtractInvoice returns a fixed valid draft.
type LLM struct {
	ChatFunc    func(ctx context.Context, req contract.ChatRequest) (contract.ChatResponse, error)
	ExtractFunc func(ctx context.Context, text string, image []byte, mimeType string) (domain.InvoiceDraft, error)

	mu       sync.Mutex
	requests []contract.ChatRequest
}

// Chat records the request and returns the scripted reply.
func (f *LLM) Chat(ctx context.Context, req contract.ChatRequest) (contract.ChatResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	fn := f.ChatFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	last := ""
	for i := len(req.History) - 1; i >= 0; i-- {
		if req.History[i].Role == "user" && req.History[i].Text != "" {
			last = req.History[i].Text
			break
		}
	}
	return contract.ChatResponse{Text: "Fake model reply to: " + last, Model: "fake"}, nil
}

// ExtractInvoice returns the scripted draft.
func (f *LLM) ExtractInvoice(ctx context.Context, text string, image []byte, mimeType string) (domain.InvoiceDraft, error) {
	if f.ExtractFunc != nil {
		return f.ExtractFunc(ctx, text, image, mimeType)
	}
	return SampleDraft(), nil
}

// Requests returns a copy of every ChatRequest received so far.
func (f *LLM) Requests() []contract.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.ChatRequest(nil), f.requests...)
}

// SampleDraft is the draft from step 2 of the demo script.
func SampleDraft() domain.InvoiceDraft {
	return domain.InvoiceDraft{
		ClientName:  "Jane",
		ClientEmail: "jane@example.com",
		Currency:    "USD",
		Items:       []domain.LineItem{{Name: "Landing page", Quantity: 1, UnitPrice: "450.00"}},
		DueDays:     7,
	}
}
