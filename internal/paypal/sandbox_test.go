//go:build sandbox

package paypal

// These tests call the real PayPal sandbox. Run them with:
//
//	set -a; . ./.env; set +a
//	go test -tags sandbox -v ./internal/paypal
//
// They need PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET. Set
// PAYPAL_TEST_RECIPIENT to send the invoice to your sandbox Personal
// account; the default is a fake address.

import (
	"context"
	"os"
	"testing"
	"time"

	"paidup-agent/internal/domain"
)

func sandboxClient(t *testing.T) *Client {
	t.Helper()
	cfg := ConfigFromEnv(os.Getenv)
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		t.Skip("PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET are not set")
	}
	return NewClient(cfg)
}

func TestSandboxInvoiceFlow(t *testing.T) {
	c := sandboxClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	recipient := os.Getenv("PAYPAL_TEST_RECIPIENT")
	if recipient == "" {
		recipient = "jane@example.com"
	}
	d := domain.InvoiceDraft{
		ClientName:  "Jane Doe",
		ClientEmail: recipient,
		Currency:    "USD",
		DueDays:     7,
		Note:        "PaidUp sandbox test",
		Items: []domain.LineItem{
			{Name: "Landing page", Quantity: 1, UnitPrice: "450.00"},
		},
	}
	reqID := domain.NewID()

	inv, err := c.CreateDraft(ctx, d, reqID+"-create")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Logf("created %s (number %s, status %s)", inv.ID, inv.Number, inv.Status)
	if inv.Status != "DRAFT" || inv.Total != "450.00" {
		t.Fatalf("draft = %+v", inv)
	}

	again, err := c.CreateDraft(ctx, d, reqID+"-create")
	if err != nil {
		t.Fatalf("repeat create: %v", err)
	}
	if again.ID != inv.ID {
		t.Errorf("same PayPal-Request-Id gave a second invoice: %s", again.ID)
	}

	if err := c.Send(ctx, inv.ID, reqID+"-send"); err != nil {
		t.Fatalf("send: %v", err)
	}
	sent, err := c.Get(ctx, inv.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	t.Logf("after send: status %s, payer link %q", sent.Status, sent.PayerViewURL)
	if sent.Status != "SENT" && sent.Status != "UNPAID" {
		t.Errorf("status after send = %s", sent.Status)
	}
	if sent.PayerViewURL == "" {
		t.Error("no payer link after send: check the metadata field name")
	}

	if err := c.Remind(ctx, inv.ID, "Friendly reminder",
		"PaidUp sandbox test", reqID+"-remind"); err != nil {
		t.Errorf("remind: %v", err)
	}

	list, err := c.List(ctx, sent.Status, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, x := range list {
		found = found || x.ID == inv.ID
	}
	if !found {
		t.Logf("invoice %s is not in the first page of %s invoices yet "+
			"(search can lag)", inv.ID, sent.Status)
	}

	if err := c.Cancel(ctx, inv.ID, "Sandbox test clean-up",
		reqID+"-cancel"); err != nil {
		t.Errorf("cancel: %v", err)
	}
}
