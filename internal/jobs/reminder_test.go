package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
	"paidup-agent/internal/fakes"
)

type env struct {
	job   *ReminderJob
	pp    *fakes.PayPal
	store *fakes.Store
	llm   *fakes.LLM
	bus   *fakes.Bus
	now   time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{pp: fakes.NewPayPal(), store: fakes.NewStore(),
		llm: &fakes.LLM{}, bus: fakes.NewBus()}
	e.now = time.Now().UTC()
	clock := func() time.Time { return e.now }
	e.store.Clock = clock
	e.llm.ChatFunc = func(context.Context, contract.ChatRequest) (contract.ChatResponse, error) {
		return contract.ChatResponse{Text: "Hi Jane, invoice 0001 is now overdue. Please pay soon."}, nil
	}
	e.job = NewReminderJob(e.pp, e.store, e.llm, e.bus)
	e.job.Now = clock
	e.job.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return e
}

func (e *env) sendInvoice(t *testing.T) domain.Invoice {
	t.Helper()
	ctx := context.Background()
	inv, err := e.pp.CreateDraft(ctx, fakes.SampleDraft(), domain.NewID())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.pp.Send(ctx, inv.ID, domain.NewID()); err != nil {
		t.Fatal(err)
	}
	inv, _ = e.pp.Get(ctx, inv.ID)
	return inv
}

func (e *env) run(t *testing.T) int {
	t.Helper()
	n, err := e.job.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOneOpenProposalPerOverdueInvoice(t *testing.T) {
	e := newEnv(t)
	inv := e.sendInvoice(t) // due in 7 days
	sub, stop := e.bus.Subscribe()
	defer stop()

	if n := e.run(t); n != 0 {
		t.Fatalf("not yet overdue: %d cards", n)
	}

	e.now = e.now.AddDate(0, 0, 10)
	if n := e.run(t); n != 1 {
		t.Fatalf("overdue: %d cards, want 1", n)
	}
	if n := e.run(t); n != 0 {
		t.Fatalf("second scan: %d cards, want 0 (one is open)", n)
	}

	ev := <-sub
	var a domain.PendingAction
	_ = json.Unmarshal(ev.Data, &a)
	if ev.Type != domain.EventActionProposed || a.SessionID != "" ||
		a.InvoiceID != inv.ID || a.Tool != domain.ToolProposeReminder ||
		!a.ExpiresAt.Equal(e.now.Add(72*time.Hour)) {
		t.Fatalf("card = %+v", a)
	}
	var args struct{ Subject, Note string }
	_ = json.Unmarshal(a.Args, &args)
	if !strings.Contains(args.Subject, "0001") ||
		!strings.Contains(args.Note, "overdue") {
		t.Fatalf("args = %+v", args)
	}
	if e.pp.WriteCalls() != 2 { // only the create and send from the setup
		t.Fatal("the job sent something to PayPal")
	}
}

func TestCooldownAfterSentReminder(t *testing.T) {
	e := newEnv(t)
	inv := e.sendInvoice(t)
	e.now = e.now.AddDate(0, 0, 10)
	ctx := context.Background()

	// The user approved a reminder one day ago.
	_ = e.store.AddReminder(ctx, inv.ID, "sent")
	e.now = e.now.Add(24 * time.Hour)
	if n := e.run(t); n != 0 {
		t.Fatalf("inside cooldown: %d cards", n)
	}
	e.now = e.now.Add(48 * time.Hour)
	if n := e.run(t); n != 1 {
		t.Fatalf("after cooldown: %d cards, want 1", n)
	}
}

func TestExpiredCardAllowsANewOne(t *testing.T) {
	e := newEnv(t)
	e.sendInvoice(t)
	e.now = e.now.AddDate(0, 0, 10)
	if e.run(t) != 1 {
		t.Fatal("first card missing")
	}
	e.now = e.now.Add(73 * time.Hour)
	if n := e.run(t); n != 1 {
		t.Fatalf("after expiry: %d cards, want 1", n)
	}
}

func TestTemplateWhenGeminiIsBusy(t *testing.T) {
	e := newEnv(t)
	e.sendInvoice(t)
	e.now = e.now.AddDate(0, 0, 10)
	e.llm.ChatFunc = func(context.Context, contract.ChatRequest) (contract.ChatResponse, error) {
		return contract.ChatResponse{}, contract.ErrBusy
	}
	if n := e.run(t); n != 1 {
		t.Fatalf("cards = %d", n)
	}
	acts, _ := e.store.ListActions(context.Background(), domain.ActionPending)
	var args struct{ Note string }
	_ = json.Unmarshal(acts[0].Args, &args)
	if !strings.Contains(args.Note, "friendly reminder") {
		t.Fatalf("note = %q", args.Note)
	}
}

// failingList makes PayPal List fail so the job uses the cache.
type failingList struct{ *fakes.PayPal }

func (failingList) List(context.Context, string, int) ([]domain.Invoice, error) {
	return nil, errors.New("paypal down")
}

func TestUsesCacheWhenPayPalIsDown(t *testing.T) {
	e := newEnv(t)
	inv := e.sendInvoice(t)
	_ = e.store.UpsertInvoice(context.Background(), inv)
	e.job.pp = failingList{e.pp}
	e.now = e.now.AddDate(0, 0, 10)
	if n := e.run(t); n != 1 {
		t.Fatalf("cards = %d, want 1 from the cache", n)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	e := newEnv(t)
	e.job.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.job.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
