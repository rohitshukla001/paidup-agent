package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
	"paidup-agent/internal/fakes"
)

// countingPayPal counts Get calls and can fail VerifyWebhook.
type countingPayPal struct {
	*fakes.PayPal
	gets      int
	verifyErr error
}

func (c *countingPayPal) Get(ctx context.Context, id string) (domain.Invoice, error) {
	c.gets++
	return c.PayPal.Get(ctx, id)
}

func (c *countingPayPal) VerifyWebhook(ctx context.Context, h http.Header,
	raw []byte) (bool, error) {
	if c.verifyErr != nil {
		return false, c.verifyErr
	}
	return c.PayPal.VerifyWebhook(ctx, h, raw)
}

type env struct {
	h     http.Handler
	pp    *countingPayPal
	store *fakes.Store
	bus   *fakes.Bus
	sub   <-chan domain.Event
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{pp: &countingPayPal{PayPal: fakes.NewPayPal()},
		store: fakes.NewStore(), bus: fakes.NewBus()}
	h := New(e.pp, e.store, e.bus).(*Handler)
	h.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	e.h = h
	var stop func()
	e.sub, stop = e.bus.Subscribe()
	t.Cleanup(stop)
	return e
}

func (e *env) post(body string) int {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/webhooks/paypal",
		strings.NewReader(body)))
	return rec.Code
}

func (e *env) sentInvoice(t *testing.T) domain.Invoice {
	t.Helper()
	ctx := context.Background()
	inv, _ := e.pp.CreateDraft(ctx, fakes.SampleDraft(), domain.NewID())
	_ = e.pp.Send(ctx, inv.ID, domain.NewID())
	return inv
}

func paidEvent(eventID, invoiceID string) string {
	return `{"id":"` + eventID + `","event_type":"INVOICING.INVOICE.PAID",` +
		`"resource":{"invoice":{"id":"` + invoiceID + `","status":"PAID"}}}`
}

func TestPaidEventUpdatesCacheAndPublishes(t *testing.T) {
	e := newEnv(t)
	inv := e.sentInvoice(t)
	_, _ = e.pp.MarkPaid(inv.ID)

	if code := e.post(paidEvent("WH-1", inv.ID)); code != 200 {
		t.Fatalf("status = %d", code)
	}
	cached, _ := e.store.ListInvoices(context.Background(), "PAID")
	if len(cached) != 1 || cached[0].ID != inv.ID {
		t.Fatalf("cache = %+v", cached)
	}
	select {
	case ev := <-e.sub:
		var got domain.Invoice
		_ = json.Unmarshal(ev.Data, &got)
		if ev.Type != domain.EventInvoiceUpdated || got.Status != "PAID" {
			t.Fatalf("event = %s %+v", ev.Type, got)
		}
	case <-time.After(time.Second):
		t.Fatal("no invoice.updated")
	}
}

func TestDuplicateEventHasNoEffect(t *testing.T) {
	e := newEnv(t)
	inv := e.sentInvoice(t)
	body := paidEvent("WH-dup", inv.ID)
	if e.post(body) != 200 || e.post(body) != 200 {
		t.Fatal("want 200 for both deliveries")
	}
	if e.pp.gets != 1 {
		t.Fatalf("PayPal Get calls = %d, want 1", e.pp.gets)
	}
	<-e.sub
	select {
	case ev := <-e.sub:
		t.Fatalf("duplicate published %s", ev.Type)
	default:
	}
}

func TestRejectsBadRequests(t *testing.T) {
	e := newEnv(t)
	e.pp.RejectWebhooks = true
	if code := e.post(paidEvent("WH-2", "x")); code != 400 {
		t.Fatalf("bad signature: %d", code)
	}
	if first, _ := e.store.SaveEventOnce(context.Background(), "WH-2", "", nil); !first {
		t.Fatal("rejected event was saved")
	}

	e.pp.RejectWebhooks = false
	e.pp.verifyErr = errors.New("paypal down")
	if code := e.post(paidEvent("WH-3", "x")); code != 500 {
		t.Fatalf("verify error: %d", code)
	}

	e.pp.verifyErr = nil
	if code := e.post(`{"event_type":"X"}`); code != 400 {
		t.Fatalf("no event ID: %d", code)
	}
	big := `{"id":"WH-4","pad":"` + strings.Repeat("x", maxBody) + `"}`
	if code := e.post(big); code != 413 {
		t.Fatalf("too large: %d", code)
	}
}

func TestOtherEventsAreSavedOnly(t *testing.T) {
	e := newEnv(t)
	if code := e.post(`{"id":"WH-5","event_type":"PAYMENT.CAPTURE.COMPLETED",` +
		`"resource":{"id":"CAP-1"}}`); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if e.pp.gets != 0 {
		t.Fatal("non-invoice event called PayPal")
	}
	if first, _ := e.store.SaveEventOnce(context.Background(), "WH-5", "", nil); first {
		t.Fatal("event was not saved")
	}
}

func TestMissingInvoiceStillReturns200(t *testing.T) {
	e := newEnv(t)
	if code := e.post(paidEvent("WH-6", "INV2-AAAA-BBBB-CCCC-DDDD")); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if code := e.post(`{"id":"WH-7","event_type":"INVOICING.INVOICE.PAID",` +
		`"resource":{}}`); code != 200 {
		t.Fatalf("no ID: %d", code)
	}
}

func TestInvoiceID(t *testing.T) {
	tests := map[string]string{
		`{"invoice":{"id":"INV2-AAAA-BBBB-CCCC-DDDD"}}`:                                                                       "INV2-AAAA-BBBB-CCCC-DDDD",
		`{"id":"INV2-WXYZ-1234-ABCD-5678","status":"PAID"}`:                                                                   "INV2-WXYZ-1234-ABCD-5678",
		`{"id":"PAY-1","links":[{"href":"https://api-m.sandbox.paypal.com/v2/invoicing/invoices/INV2-Q1W2-E3R4-T5Y6-U7I8"}]}`: "INV2-Q1W2-E3R4-T5Y6-U7I8",
		`{"id":"CAP-1"}`: "",
		`not json`:       "",
	}
	for in, want := range tests {
		if got := InvoiceID(json.RawMessage(in)); got != want {
			t.Errorf("InvoiceID(%s) = %q, want %q", in, got, want)
		}
	}
}

var _ contract.PayPal = (*countingPayPal)(nil)
