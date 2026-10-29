// Package httpapi serves the REST API, the SSE streams, and the Angular
// files.
//
// Task 6 owns this package. Export:
//
//	httpapi.New(cfg httpapi.Config, a contract.Agent, s contract.Store,
//		p contract.PayPal, b contract.Bus, webhook http.Handler) http.Handler
//
// See Task 6 in docs/implementation-plan.md.
package httpapi
