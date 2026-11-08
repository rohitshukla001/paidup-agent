// Package webhook receives PayPal webhook events.
//
// Task 7 owns this package. The handler verifies the signature with
// PayPal, saves the event ID so a repeat has no effect, gets the invoice
// from PayPal, updates the cache, and publishes invoice.updated.
//
// Testing note: events from the webhook simulator in the PayPal dashboard
// can fail the signature check. Test the success path with a real sandbox
// payment.
package webhook
