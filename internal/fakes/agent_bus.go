package fakes

import (
	"context"
	"sync"

	"paidup-agent/internal/domain"
)

// Agent is a fake of contract.Agent. It echoes the user text and approves
// every action without a PayPal call. Use it to build the HTTP API and the UI.
type Agent struct{}

func (Agent) HandleMessage(_ context.Context, _ string, text string, _ []byte, _ string, emit func(domain.Event)) error {
	if e, err := domain.NewEvent(domain.EventMessageDelta, map[string]string{"text": "Fake agent received: " + text}); err == nil {
		emit(e)
	}
	if e, err := domain.NewEvent(domain.EventMessageDone, map[string]string{"message_id": domain.NewID()}); err == nil {
		emit(e)
	}
	return nil
}

func (Agent) Approve(_ context.Context, _ string, actionID string, emit func(domain.Event)) error {
	if e, err := domain.NewEvent(domain.EventActionResult, map[string]string{"action_id": actionID, "status": string(domain.ActionDone)}); err == nil {
		emit(e)
	}
	return nil
}

func (Agent) Reject(context.Context, string, string) error { return nil }

// Bus is an in-memory fan-out of events. A slow subscriber loses events
// instead of blocking the publisher.
type Bus struct {
	mu   sync.Mutex
	subs map[chan domain.Event]struct{}
}

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{subs: map[chan domain.Event]struct{}{}} }

// Publish sends e to every subscriber that has room in its buffer.
func (b *Bus) Publish(e domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a buffered channel and a function that closes it.
func (b *Bus) Subscribe() (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, 32)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[chan domain.Event]struct{}{}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
}
