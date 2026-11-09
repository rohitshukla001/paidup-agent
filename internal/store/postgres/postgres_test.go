//go:build pg

package postgres

// These tests need a Postgres database that they may wipe. Start one with
// "docker compose up -d db", then run:
//
//	go test -tags pg ./internal/store/...
//
// Set PAIDUP_TEST_DATABASE_URL to use another database.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("PAIDUP_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://paidup:paidup@localhost:5432/paidup_test?sslmode=disable"
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrationsRunOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n < 1 {
		t.Fatalf("schema_migrations rows = %d, err = %v", n, err)
	}
}

func TestSessionsAndAIRequests(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, err := s.CreateSession(ctx)
	if err != nil || len(id) != 36 {
		t.Fatalf("session = %q, %v", id, err)
	}
	for range 3 {
		if err := s.RecordAIRequest(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.CountAIRequests(ctx, id, time.Now().Add(-time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("count = %d, %v", n, err)
	}
	if n, _ := s.CountAIRequests(ctx, id, time.Now().Add(time.Minute)); n != 0 {
		t.Fatalf("future window count = %d", n)
	}
}

func TestMessagesKeepOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	same := time.Now()
	for i, role := range []string{"user", "model", "tool", "user"} {
		if err := s.AddMessage(ctx, domain.Message{SessionID: "s1", Role: role,
			Content: string(rune('a' + i)), CreatedAt: same}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.AddMessage(ctx, domain.Message{SessionID: "other", Role: "user", Content: "x"})

	got, err := s.RecentMessages(ctx, "s1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Content != "b" || got[2].Content != "d" ||
		got[0].Role != "model" || got[0].ID == "" {
		t.Fatalf("messages = %+v", got)
	}
}

func newAction(id string) domain.PendingAction {
	return domain.PendingAction{
		ID: id, SessionID: "s1", Tool: domain.ToolProposeReminder,
		InvoiceID: "INV-1", Args: json.RawMessage(`{"note":"pay"}`),
		Preview: json.RawMessage(`{"kind":"reminder"}`),
		Status:  domain.ActionPending, RequestID: "rq-" + id,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond),
	}
}

func TestActions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := newAction("a1")
	if err := s.SaveAction(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAction(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tool != a.Tool || string(got.Args) != `{"note": "pay"}` ||
		got.Status != domain.ActionPending || !got.ExpiresAt.Equal(a.ExpiresAt) ||
		got.Result != nil {
		t.Fatalf("action = %+v", got)
	}
	if _, err := s.GetAction(ctx, "nope"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("missing action: %v", err)
	}

	if has, _ := s.HasPendingAction(ctx, a.Tool, "INV-1"); !has {
		t.Fatal("pending action not found")
	}
	expired := newAction("a2")
	expired.InvoiceID = "INV-2"
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	_ = s.SaveAction(ctx, expired)
	if has, _ := s.HasPendingAction(ctx, a.Tool, "INV-2"); has {
		t.Fatal("expired action counted as pending")
	}

	pending, _ := s.ListActions(ctx, domain.ActionPending)
	all, _ := s.ListActions(ctx, "")
	if len(pending) != 2 || len(all) != 2 || pending[0].ID != "a2" {
		t.Fatalf("pending = %d, all = %d", len(pending), len(all))
	}

	if err := s.SetActionResult(ctx, "a1", domain.ActionDone,
		json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAction(ctx, "a1")
	if got.Status != domain.ActionDone || string(got.Result) != `{"ok": true}` {
		t.Fatalf("after result = %+v", got)
	}
	if err := s.SetActionResult(ctx, "nope", domain.ActionDone, nil); !errors.Is(err,
		contract.ErrNotFound) {
		t.Fatalf("result on missing: %v", err)
	}
	if err := s.SaveAction(ctx, domain.PendingAction{}); !errors.Is(err,
		contract.ErrValidation) {
		t.Fatalf("save without ID: %v", err)
	}
}

func TestTransitionActionIsAtomic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_ = s.SaveAction(ctx, newAction("race"))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.TransitionAction(ctx, "race", domain.ActionPending,
				domain.ActionApproved)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d callers won, want 1", wins.Load())
	}
	if _, err := s.TransitionAction(ctx, "nope", domain.ActionPending,
		domain.ActionApproved); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("missing action: %v", err)
	}
}

func TestInvoiceCache(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	inv := domain.Invoice{ID: "INV-1", Number: "0001", Status: "SENT",
		ClientName: "Jane", ClientEmail: "jane@example.com", Currency: "USD",
		Total: "450.00", DueDate: time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC),
		PayerViewURL: "https://pay/1", UpdatedAt: t0}
	if err := s.UpsertInvoice(ctx, inv); err != nil {
		t.Fatal(err)
	}

	paid := inv
	paid.Status, paid.PayerViewURL, paid.UpdatedAt = "PAID", "", t0.Add(time.Minute)
	_ = s.UpsertInvoice(ctx, paid)

	stale := inv
	stale.Status, stale.UpdatedAt = "SENT", t0.Add(30*time.Second)
	_ = s.UpsertInvoice(ctx, stale) // older than the PAID copy: ignored

	_ = s.UpsertInvoice(ctx, domain.Invoice{ID: "INV-2", Number: "0002",
		Status: "DRAFT", UpdatedAt: t0})

	got, err := s.ListInvoices(ctx, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	one := got[1]
	if one.ID != "INV-1" || one.Status != "PAID" ||
		one.PayerViewURL != "https://pay/1" || !one.DueDate.Equal(inv.DueDate) ||
		!one.UpdatedAt.Equal(paid.UpdatedAt) {
		t.Fatalf("cached = %+v", one)
	}
	if !got[0].DueDate.IsZero() {
		t.Fatalf("NULL due date = %v", got[0].DueDate)
	}
	if paidOnly, _ := s.ListInvoices(ctx, "PAID"); len(paidOnly) != 1 {
		t.Fatalf("PAID filter = %d", len(paidOnly))
	}
}

func TestCancelReplacesSentWithALaterTime(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	t1 := time.Date(2026, 11, 9, 14, 47, 0, 0, time.UTC)
	sent := domain.Invoice{ID: "INV-2", Number: "0002", Status: "SENT",
		Currency: "USD", Total: "1580.00", UpdatedAt: t1.Add(3 * time.Minute)}
	_ = s.UpsertInvoice(ctx, sent)
	cancelled := sent
	cancelled.Status, cancelled.UpdatedAt = "CANCELLED", t1
	_ = s.UpsertInvoice(ctx, cancelled)

	got, _ := s.ListInvoices(ctx, "")
	if len(got) != 1 || got[0].Status != "CANCELLED" {
		t.Fatalf("cached = %+v, want CANCELLED", got)
	}
	stale := sent
	stale.UpdatedAt = t1.Add(time.Hour)
	_ = s.UpsertInvoice(ctx, stale)
	if got, _ := s.ListInvoices(ctx, ""); got[0].Status != "CANCELLED" {
		t.Fatalf("a later SENT copy replaced CANCELLED: %+v", got[0])
	}
}

func TestInvoiceOrderIsNumeric(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, n := range []string{"9", "10", "2"} {
		_ = s.UpsertInvoice(ctx, domain.Invoice{ID: "INV-" + n, Number: n,
			Status: "SENT", UpdatedAt: time.Now()})
	}
	got, _ := s.ListInvoices(ctx, "")
	if len(got) != 3 || got[0].Number != "10" || got[1].Number != "9" ||
		got[2].Number != "2" {
		t.Fatalf("order = %v", got)
	}
}

func TestEventsRemindersReset(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	raw := []byte("{\n \"id\": \"WH-1\"\n}")
	if first, err := s.SaveEventOnce(ctx, "WH-1", "INVOICING.INVOICE.PAID", raw); err != nil || !first {
		t.Fatalf("first = %v, %v", first, err)
	}
	if again, _ := s.SaveEventOnce(ctx, "WH-1", "INVOICING.INVOICE.PAID", raw); again {
		t.Fatal("duplicate event saved twice")
	}
	var stored []byte
	_ = s.db.QueryRowContext(ctx, `SELECT payload FROM webhook_events
		WHERE event_id = 'WH-1'`).Scan(&stored)
	if string(stored) != string(raw) {
		t.Fatalf("payload changed: %q", stored)
	}

	if _, ok, _ := s.LastReminder(ctx, "INV-1"); ok {
		t.Fatal("reminder before any was added")
	}
	_ = s.AddReminder(ctx, "INV-1", "first")
	_ = s.AddReminder(ctx, "INV-1", "second")
	last, ok, err := s.LastReminder(ctx, "INV-1")
	if err != nil || !ok || time.Since(last) > time.Minute {
		t.Fatalf("last = %v, %v, %v", last, ok, err)
	}

	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if first, _ := s.SaveEventOnce(ctx, "WH-1", "x", raw); !first {
		t.Fatal("Reset kept events")
	}
}
