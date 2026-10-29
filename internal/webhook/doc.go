// Package webhook receives PayPal webhook events.
//
// Task 7 owns this package. Export:
//
//	webhook.New(p contract.PayPal, s contract.Store, b contract.Bus) http.Handler
//
// See Task 7 in docs/implementation-plan.md.
package webhook
