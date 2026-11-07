// Package events is the in-memory event bus for live dashboard updates.
//
// Task 6 owns this package. The bus lives in one server process. That is
// enough for one Render instance; more instances would need a shared bus.
package events
