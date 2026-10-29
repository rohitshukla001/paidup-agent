package fakes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

func TestPayPalFlowAndIdempotency(t *testing.T) {
	ctx := context.Background()
	pp := NewPayPal()

	inv, err := pp.CreateDraft(ctx, SampleDraft(), "r1-create")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := pp.CreateDraft(ctx, SampleDraft(), "r1-create")
	if again.ID != inv.ID || pp.WriteCalls() != 1 {
		t.Fatalf("repeated request ID made a second invoice: %s vs %s, calls %d", again.ID, inv.ID, pp.WriteCalls())
	}
	if inv.Status != "DRAFT" || inv.Total != "450.00" {
		t.Fatalf("draft = %+v", inv)
	}

	if err := pp.Send(ctx, inv.ID, "r1-send"); err != nil {
		t.Fatal(err)
	}
	if err := pp.Send(ctx, inv.ID, "r1-send"); err != nil {
		t.Fatalf("repeated send with same request ID: %v", err)
	}
	got, _ := pp.Get(ctx, inv.ID)
	if got.Status != "SENT" || got.PayerViewURL == "" {
		t.Fatalf("after send = %+v", got)
	}
	if err := pp.Remind(ctx, inv.ID, "s", "n", "r2-remind"); err != nil {
		t.Fatal(err)
	}
	if _, err := pp.MarkPaid(inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := pp.Remind(ctx, inv.ID, "s", "n", "r3-remind"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("remind on PAID: want ErrValidation, got %v", err)
	}
	if _, err := pp.Get(ctx, "nope"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	paid, _ := pp.List(ctx, "PAID", 10)
	if len(paid) != 1 {
		t.Fatalf("List(PAID) = %d invoices", len(paid))
	}
	found, _ := pp.Search(ctx, "jane@example.com", time.Time{}, time.Time{})
	if len(found) != 1 {
		t.Fatalf("Search = %d invoices", len(found))
	}
}

func TestPayPalRejectsInvalidDraft(t *testing.T) {
	d := SampleDraft()
	d.Currency = "XYZ"
	if _, err := NewPayPal().CreateDraft(context.Background(), d, "r"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("want ErrValidation, got %v", err)
	}
}

func TestStoreTransitionIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	a := domain.PendingAction{ID: domain.NewID(), Tool: domain.ToolProposeInvoice, Status: domain.ActionPending}
	if err := s.SaveAction(ctx, a); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.TransitionAction(ctx, a.ID, domain.ActionPending, domain.ActionApproved)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d transitions won, want 1", wins)
	}
}

func TestStoreEventsRemindersAndPending(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	if first, _ := s.SaveEventOnce(ctx, "WH-1", "INVOICING.INVOICE.PAID", nil); !first {
		t.Fatal("first save returned false")
	}
	if again, _ := s.SaveEventOnce(ctx, "WH-1", "INVOICING.INVOICE.PAID", nil); again {
		t.Fatal("duplicate save returned true")
	}
	if _, ok, _ := s.LastReminder(ctx, "INV-1"); ok {
		t.Fatal("reminder found before any was added")
	}
	_ = s.AddReminder(ctx, "INV-1", "note")
	if _, ok, _ := s.LastReminder(ctx, "INV-1"); !ok {
		t.Fatal("reminder not found")
	}
	_ = s.SaveAction(ctx, domain.PendingAction{ID: "a1", Tool: domain.ToolProposeReminder, InvoiceID: "INV-1", Status: domain.ActionPending})
	if has, _ := s.HasPendingAction(ctx, domain.ToolProposeReminder, "INV-1"); !has {
		t.Fatal("pending reminder not found")
	}
	if pending, _ := s.ListActions(ctx, domain.ActionPending); len(pending) != 1 {
		t.Fatalf("ListActions(PENDING) = %d", len(pending))
	}
	_ = s.Reset(ctx)
	if has, _ := s.HasPendingAction(ctx, domain.ToolProposeReminder, "INV-1"); has {
		t.Fatal("Reset kept an action")
	}
}

func TestBusFanOut(t *testing.T) {
	b := NewBus()
	ch1, stop1 := b.Subscribe()
	ch2, stop2 := b.Subscribe()
	defer stop2()

	e, _ := domain.NewEvent(domain.EventInvoiceUpdated, domain.Invoice{ID: "INV-1"})
	b.Publish(e)
	for _, ch := range []<-chan domain.Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != domain.EventInvoiceUpdated {
				t.Fatalf("type = %s", got.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("no event")
		}
	}
	stop1()
	stop1()      // second call is safe
	b.Publish(e) // must not panic on the closed subscriber
}

func TestFakeLLMEcho(t *testing.T) {
	f := &LLM{}
	resp, err := f.Chat(context.Background(), contract.ChatRequest{History: []contract.Turn{{Role: "user", Text: "hi"}}})
	if err != nil || resp.Text != "Fake model reply to: hi" || len(f.Requests()) != 1 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	d, _ := f.ExtractInvoice(context.Background(), "", nil, "")
	if err := domain.ValidateDraft(d); err != nil {
		t.Fatalf("SampleDraft is not valid: %v", err)
	}
}
