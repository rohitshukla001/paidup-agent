package events

import (
	"sync"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// bufferSize is the events a subscriber can fall behind before it loses
// events. A dashboard that loses one reloads its data on the next event.
const bufferSize = 64

// Bus fans out each published event to every subscriber.
type Bus struct {
	mu   sync.Mutex
	subs map[chan domain.Event]struct{}
}

var _ contract.Bus = (*Bus)(nil)

// NewBus returns an empty bus.
func NewBus() contract.Bus { return &Bus{subs: map[chan domain.Event]struct{}{}} }

// Publish never blocks: a subscriber with a full buffer misses the event.
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

// Subscribe returns a channel of events and a function that ends the
// subscription and closes the channel. The function is safe to call more
// than one time.
func (b *Bus) Subscribe() (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, bufferSize)
	b.mu.Lock()
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

// Subscribers returns the number of open subscriptions.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
