package llm

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"paidup-agent/internal/contract"
)

// limiter allows at most max calls in any sliding window. It protects the
// whole project quota, which per-session limits cannot do.
type limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	calls []time.Time // oldest first
}

func newLimiter(max int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{max: max, window: window, now: now}
}

// wait takes one slot. When the window is full it waits for the oldest
// call to leave the window, but only up to maxWait; after that it
// returns contract.ErrBusy at once.
func (l *limiter) wait(ctx context.Context, maxWait time.Duration) error {
	if l.max <= 0 {
		return nil
	}
	for {
		l.mu.Lock()
		now := l.now()
		cut := now.Add(-l.window)
		i := 0
		for i < len(l.calls) && !l.calls[i].After(cut) {
			i++
		}
		l.calls = l.calls[i:]
		if len(l.calls) < l.max {
			l.calls = append(l.calls, now)
			l.mu.Unlock()
			return nil
		}
		free := l.calls[0].Add(l.window).Sub(now)
		l.mu.Unlock()

		if free > maxWait {
			return fmt.Errorf("%w: %d Gemini calls in the last %s",
				contract.ErrBusy, l.max, l.window)
		}
		if err := sleep(ctx, free); err != nil {
			return err
		}
		maxWait -= free
	}
}

// lru is a small least-recently-used cache for ExtractInvoice results.
type lru struct {
	max   int
	mu    sync.Mutex
	order *list.List // front is newest
	items map[string]*list.Element
}

type lruEntry struct {
	key string
	val any
}

func newLRU(max int) *lru {
	return &lru{max: max, order: list.New(), items: map[string]*list.Element{}}
}

func (c *lru) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*lruEntry).val, true
}

func (c *lru) put(key string, val any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.Value.(*lruEntry).val = val
		c.order.MoveToFront(e)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry{key: key, val: val})
	for c.order.Len() > c.max {
		old := c.order.Back()
		c.order.Remove(old)
		delete(c.items, old.Value.(*lruEntry).key)
	}
}

func (c *lru) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
