// Package paypal is the PayPal REST client for invoices and webhooks.
//
// Task 2 owns this package. It implements contract.PayPal against the
// Invoicing v2 API. Every write call sends a PayPal-Request-Id header, so
// a retry with the same request ID cannot create a second invoice.
//
// Unit tests use a local stand-in for PayPal. Tests against the real
// sandbox sit behind the "sandbox" build tag; see sandbox_test.go.
package paypal
