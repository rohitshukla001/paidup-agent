package events

import (
	"sync"
	"testing"
	"time"

	"paidup-agent/internal/domain"
)

func TestFanOutAndUnsubscribe(t *testing.T) {
	b := NewBus().(*Bus)
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
	stop1()
	if _, open := <-ch1; open {
		t.Fatal("channel still open after stop")
	}
	if b.Subscribers() != 1 {
		t.Fatalf("subscribers = %d", b.Subscribers())
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBus()
	_, stop := b.Subscribe() // never read
	defer stop()
	e, _ := domain.NewEvent(domain.EventInvoiceUpdated, nil)
	done := make(chan struct{})
	go func() {
		for range bufferSize * 3 {
			b.Publish(e)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
}

func TestConcurrentUse(t *testing.T) {
	b := NewBus()
	e, _ := domain.NewEvent(domain.EventInvoiceUpdated, nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, stop := b.Subscribe()
			stop()
		}()
		go func() {
			defer wg.Done()
			b.Publish(e)
		}()
	}
	wg.Wait()
}
