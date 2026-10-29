package fakes

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// PayPal is an in-memory fake of contract.PayPal.
//
// A repeated requestID returns the first result without a second change,
// like the PayPal-Request-Id header. WriteCalls counts calls that changed
// state.
type PayPal struct {
	Clock Clock
	// RejectWebhooks makes VerifyWebhook return false.
	RejectWebhooks bool

	mu         sync.Mutex
	invoices   map[string]domain.Invoice
	seen       map[string]domain.Invoice // requestID -> result
	seq        int
	writeCalls int
}

// NewPayPal returns an empty fake.
func NewPayPal() *PayPal { return &PayPal{} }

func (f *PayPal) init() {
	if f.invoices == nil {
		f.invoices = map[string]domain.Invoice{}
		f.seen = map[string]domain.Invoice{}
	}
}

// CreateDraft stores a DRAFT invoice.
func (f *PayPal) CreateDraft(_ context.Context, d domain.InvoiceDraft, requestID string) (domain.Invoice, error) {
	if err := domain.ValidateDraft(d); err != nil {
		return domain.Invoice{}, err
	}
	total, err := domain.DraftTotalCents(d)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("%w: %v", contract.ErrValidation, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if inv, ok := f.seen[requestID]; ok {
		return inv, nil
	}
	f.seq++
	now := f.Clock.now()
	inv := domain.Invoice{
		ID:          fmt.Sprintf("INV2-FAKE-%04d", f.seq),
		Number:      fmt.Sprintf("%04d", f.seq),
		Status:      "DRAFT",
		ClientName:  d.ClientName,
		ClientEmail: d.ClientEmail,
		Currency:    d.Currency,
		Total:       domain.FormatAmount(total),
		DueDate:     d.DueDate(now),
		UpdatedAt:   now,
	}
	f.invoices[inv.ID] = inv
	f.seen[requestID] = inv
	f.writeCalls++
	return inv, nil
}

// Send moves a DRAFT invoice to SENT and gives it a payer link.
func (f *PayPal) Send(_ context.Context, invoiceID, requestID string) error {
	return f.write(invoiceID, requestID, func(inv *domain.Invoice) error {
		if inv.Status != "DRAFT" {
			return fmt.Errorf("%w: invoice %s is %s, not DRAFT", contract.ErrValidation, inv.ID, inv.Status)
		}
		inv.Status = "SENT"
		inv.PayerViewURL = "https://www.sandbox.paypal.com/invoice/p/#" + inv.ID
		return nil
	})
}

// Remind accepts a reminder for an invoice that the client still owes.
func (f *PayPal) Remind(_ context.Context, invoiceID, _, _, requestID string) error {
	return f.write(invoiceID, requestID, func(inv *domain.Invoice) error {
		switch inv.Status {
		case "SENT", "UNPAID", "PARTIALLY_PAID":
			return nil
		}
		return fmt.Errorf("%w: invoice %s is %s and cannot get a reminder", contract.ErrValidation, inv.ID, inv.Status)
	})
}

// Cancel moves a sent invoice to CANCELLED.
func (f *PayPal) Cancel(_ context.Context, invoiceID, _, requestID string) error {
	return f.write(invoiceID, requestID, func(inv *domain.Invoice) error {
		switch inv.Status {
		case "SENT", "UNPAID":
			inv.Status = "CANCELLED"
			return nil
		}
		return fmt.Errorf("%w: invoice %s is %s and cannot be cancelled", contract.ErrValidation, inv.ID, inv.Status)
	})
}

func (f *PayPal) write(invoiceID, requestID string, change func(*domain.Invoice) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if _, ok := f.seen[requestID]; ok {
		return nil
	}
	inv, ok := f.invoices[invoiceID]
	if !ok {
		return fmt.Errorf("%w: invoice %s", contract.ErrNotFound, invoiceID)
	}
	if err := change(&inv); err != nil {
		return err
	}
	inv.UpdatedAt = f.Clock.now()
	f.invoices[invoiceID] = inv
	f.seen[requestID] = inv
	f.writeCalls++
	return nil
}

// Get returns one invoice.
func (f *PayPal) Get(_ context.Context, invoiceID string) (domain.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	inv, ok := f.invoices[invoiceID]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("%w: invoice %s", contract.ErrNotFound, invoiceID)
	}
	return inv, nil
}

// List returns invoices with the status, newest number first.
func (f *PayPal) List(_ context.Context, status string, limit int) ([]domain.Invoice, error) {
	return f.filter(limit, func(inv domain.Invoice) bool { return status == "" || inv.Status == status }), nil
}

// Search returns the invoices for one client email with UpdatedAt in
// [from, to).
// A zero from or to means no bound on that side.
func (f *PayPal) Search(_ context.Context, email string, from, to time.Time) ([]domain.Invoice, error) {
	return f.filter(0, func(inv domain.Invoice) bool {
		if email != "" && inv.ClientEmail != email {
			return false
		}
		if !from.IsZero() && inv.UpdatedAt.Before(from) {
			return false
		}
		if !to.IsZero() && !inv.UpdatedAt.Before(to) {
			return false
		}
		return true
	}), nil
}

func (f *PayPal) filter(limit int, keep func(domain.Invoice) bool) []domain.Invoice {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Invoice{}
	for _, inv := range f.invoices {
		if keep(inv) {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// VerifyWebhook returns true unless RejectWebhooks is set.
func (f *PayPal) VerifyWebhook(context.Context, http.Header, []byte) (bool, error) {
	return !f.RejectWebhooks, nil
}

// MarkPaid sets an invoice to PAID, as if the client paid it.
// It exists for tests and the demo.
func (f *PayPal) MarkPaid(invoiceID string) (domain.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	inv, ok := f.invoices[invoiceID]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("%w: invoice %s", contract.ErrNotFound, invoiceID)
	}
	inv.Status = "PAID"
	inv.UpdatedAt = f.Clock.now()
	f.invoices[invoiceID] = inv
	return inv, nil
}

// WriteCalls returns the number of write calls that changed state.
func (f *PayPal) WriteCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeCalls
}
