// Package fakes holds in-memory implementations of every contract interface.
//
// Task 1 owns this package. Tests use the fakes for the interfaces that the
// task under test does not own, and cmd/server uses them until Task 9 swaps
// in the real implementations. The fakes are safe for concurrent use.
package fakes

import (
	"time"

	"paidup-agent/internal/contract"
)

// Compile-time checks that each fake satisfies its interface.
var (
	_ contract.LLM    = (*LLM)(nil)
	_ contract.PayPal = (*PayPal)(nil)
	_ contract.Store  = (*Store)(nil)
	_ contract.Agent  = (*Agent)(nil)
	_ contract.Bus    = (*Bus)(nil)
)

// Clock returns the current time. Tests replace it to control time.
type Clock func() time.Time

func (c Clock) now() time.Time {
	if c == nil {
		return time.Now().UTC()
	}
	return c()
}
