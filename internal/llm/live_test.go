//go:build gemini

package llm

// These tests call the real Gemini API. Run them with:
//
//	set -a; . ./.env; set +a
//	go test -tags gemini -v ./internal/llm
//
// They need GEMINI_API_KEY and GEMINI_MODEL_PRIMARY. Each sample costs one
// call, so the run uses 8 calls of the free-tier quota plus 2 for chat.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

type expectation struct {
	Email    string `json:"email"`
	Currency string `json:"currency"`
	Total    string `json:"total"`
	DueDays  int    `json:"due_days"`
	Invalid  string `json:"invalid"` // field that must fail validation
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	cfg := ConfigFromEnv(os.Getenv)
	if cfg.APIKey == "" || cfg.PrimaryModel == "" {
		t.Skip("GEMINI_API_KEY and GEMINI_MODEL_PRIMARY are not set")
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLiveExtractSamples(t *testing.T) {
	c := liveClient(t)
	dir := filepath.Join("..", "..", "testdata", "samples")
	raw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]expectation
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	for name, exp := range want {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			var d domain.InvoiceDraft
			if strings.HasSuffix(name, ".png") {
				d, err = c.ExtractInvoice(ctx, "", data, "image/png")
			} else {
				d, err = c.ExtractInvoice(ctx, string(data), nil, "")
			}
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			got, _ := json.Marshal(d)
			t.Logf("draft: %s", got)

			verr := domain.ValidateDraft(d)
			if exp.Invalid != "" {
				if verr == nil || !strings.Contains(verr.Error(), exp.Invalid) {
					t.Errorf("want validation error on %s, got %v", exp.Invalid, verr)
				}
			} else if verr != nil {
				t.Errorf("validation: %v", verr)
			}
			if exp.Email != "" && d.ClientEmail != exp.Email {
				t.Errorf("email = %q, want %q", d.ClientEmail, exp.Email)
			}
			if d.Currency != exp.Currency {
				t.Errorf("currency = %q, want %q", d.Currency, exp.Currency)
			}
			if total, err := domain.DraftTotalCents(d); err != nil ||
				domain.FormatAmount(total) != exp.Total {
				t.Errorf("total = %s (%v), want %s",
					domain.FormatAmount(total), err, exp.Total)
			}
			if exp.DueDays != 0 && d.DueDays != exp.DueDays {
				t.Errorf("due_days = %d, want %d", d.DueDays, exp.DueDays)
			}
		})
	}
}

func TestLiveChatToolCallRoundTrip(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	tools := []contract.ToolDecl{{
		Name:        "list_invoices",
		Description: "List the user's invoices with a PayPal status.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string",
					"description": "SENT, PAID, or DRAFT"},
			},
			"required": []string{"status"},
		},
	}}
	history := []contract.Turn{{Role: "user",
		Text: "Which invoices have I sent but not been paid for?"}}

	first, err := c.Chat(ctx, contract.ChatRequest{
		System: "Use the tools to answer.", History: history, Tools: tools})
	if errors.Is(err, contract.ErrBusy) {
		t.Skip("quota busy")
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Calls) == 0 {
		t.Fatalf("model made no tool call; text = %q", first.Text)
	}
	t.Logf("call: %s %v (signature %d bytes, model %s)", first.Calls[0].Name,
		first.Calls[0].Args, len(first.Calls[0].Signature), first.Model)

	history = append(history,
		contract.Turn{Role: "model", Text: first.Text, Calls: first.Calls},
		contract.Turn{Role: "user", Results: []contract.FuncResult{{
			Name: "list_invoices",
			Output: map[string]any{"invoices": []map[string]any{
				{"number": "0007", "client": "Jane", "total": "450.00 USD"}}},
		}}})
	second, err := c.Chat(ctx, contract.ChatRequest{
		System: "Use the tools to answer.", History: history, Tools: tools})
	if err != nil {
		t.Fatalf("second turn (signature round trip): %v", err)
	}
	if !strings.Contains(second.Text, "0007") && !strings.Contains(second.Text, "Jane") {
		t.Errorf("answer does not use the tool result: %q", second.Text)
	}
}
