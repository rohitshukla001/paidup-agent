// Package httpapi serves the REST API, the SSE streams, and the Angular
// files.
//
// Task 6 owns this package. Routes are in Section 4.5 of
// docs/implementation-plan.md. Every /api route except login, the PayPal
// webhook, and the demo reset needs the signed session cookie that login
// sets. Errors are JSON: {"code": "...", "message": "..."}.
package httpapi
