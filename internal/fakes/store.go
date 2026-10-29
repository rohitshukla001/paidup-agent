package fakes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// Store is an in-memory fake of contract.Store.
type Store struct {
	Clock Clock

	mu         sync.Mutex
	sessions   map[string]time.Time
	aiRequests map[string][]time.Time
	messages   map[string][]domain.Message
	actions    map[string]domain.PendingAction
	invoices   map[string]domain.Invoice
	events     map[string]bool
	reminders  map[string][]time.Time
}

// NewStore returns an empty fake.
func NewStore() *Store {
	s := &Store{}
	s.reset()
	return s
}

func (s *Store) reset() {
	s.sessions = map[string]time.Time{}
	s.aiRequests = map[string][]time.Time{}
	s.messages = map[string][]domain.Message{}
	s.actions = map[string]domain.PendingAction{}
	s.invoices = map[string]domain.Invoice{}
	s.events = map[string]bool{}
	s.reminders = map[string][]time.Time{}
}

func (s *Store) lock() func() {
	s.mu.Lock()
	if s.sessions == nil {
		s.reset()
	}
	return s.mu.Unlock
}

func (s *Store) CreateSession(context.Context) (string, error) {
	defer s.lock()()
	id := domain.NewID()
	s.sessions[id] = s.Clock.now()
	return id, nil
}

func (s *Store) CountAIRequests(_ context.Context, sessionID string, since time.Time) (int, error) {
	defer s.lock()()
	n := 0
	for _, t := range s.aiRequests[sessionID] {
		if !t.Before(since) {
			n++
		}
	}
	return n, nil
}

func (s *Store) RecordAIRequest(_ context.Context, sessionID string) error {
	defer s.lock()()
	s.aiRequests[sessionID] = append(s.aiRequests[sessionID], s.Clock.now())
	return nil
}

func (s *Store) AddMessage(_ context.Context, m domain.Message) error {
	defer s.lock()()
	if m.ID == "" {
		m.ID = domain.NewID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.Clock.now()
	}
	s.messages[m.SessionID] = append(s.messages[m.SessionID], m)
	return nil
}

func (s *Store) RecentMessages(_ context.Context, sessionID string, limit int) ([]domain.Message, error) {
	defer s.lock()()
	all := s.messages[sessionID]
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return append([]domain.Message(nil), all...), nil
}

func (s *Store) SaveAction(_ context.Context, a domain.PendingAction) error {
	defer s.lock()()
	if a.ID == "" {
		return fmt.Errorf("%w: action has no ID", contract.ErrValidation)
	}
	s.actions[a.ID] = a
	return nil
}

func (s *Store) GetAction(_ context.Context, id string) (domain.PendingAction, error) {
	defer s.lock()()
	a, ok := s.actions[id]
	if !ok {
		return domain.PendingAction{}, fmt.Errorf("%w: action %s", contract.ErrNotFound, id)
	}
	return a, nil
}

func (s *Store) ListActions(_ context.Context, status domain.ActionStatus) ([]domain.PendingAction, error) {
	defer s.lock()()
	out := []domain.PendingAction{}
	for _, a := range s.actions {
		if status == "" || a.Status == status {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out, nil
}

func (s *Store) TransitionAction(_ context.Context, id string, from, to domain.ActionStatus) (bool, error) {
	defer s.lock()()
	a, ok := s.actions[id]
	if !ok {
		return false, fmt.Errorf("%w: action %s", contract.ErrNotFound, id)
	}
	if a.Status != from {
		return false, nil
	}
	a.Status = to
	s.actions[id] = a
	return true, nil
}

func (s *Store) SetActionResult(_ context.Context, id string, to domain.ActionStatus, result json.RawMessage) error {
	defer s.lock()()
	a, ok := s.actions[id]
	if !ok {
		return fmt.Errorf("%w: action %s", contract.ErrNotFound, id)
	}
	a.Status = to
	a.Result = result
	s.actions[id] = a
	return nil
}

func (s *Store) HasPendingAction(_ context.Context, tool, invoiceID string) (bool, error) {
	defer s.lock()()
	for _, a := range s.actions {
		if a.Status == domain.ActionPending && a.Tool == tool && a.InvoiceID == invoiceID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) UpsertInvoice(_ context.Context, inv domain.Invoice) error {
	defer s.lock()()
	s.invoices[inv.ID] = inv
	return nil
}

func (s *Store) ListInvoices(_ context.Context, status string) ([]domain.Invoice, error) {
	defer s.lock()()
	out := []domain.Invoice{}
	for _, inv := range s.invoices {
		if status == "" || inv.Status == status {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

func (s *Store) SaveEventOnce(_ context.Context, eventID, _ string, _ []byte) (bool, error) {
	defer s.lock()()
	if s.events[eventID] {
		return false, nil
	}
	s.events[eventID] = true
	return true, nil
}

func (s *Store) AddReminder(_ context.Context, invoiceID, _ string) error {
	defer s.lock()()
	s.reminders[invoiceID] = append(s.reminders[invoiceID], s.Clock.now())
	return nil
}

func (s *Store) LastReminder(_ context.Context, invoiceID string) (time.Time, bool, error) {
	defer s.lock()()
	r := s.reminders[invoiceID]
	if len(r) == 0 {
		return time.Time{}, false, nil
	}
	return r[len(r)-1], true, nil
}

func (s *Store) Reset(context.Context) error {
	defer s.lock()()
	s.reset()
	return nil
}
