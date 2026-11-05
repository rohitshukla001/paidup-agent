// Package llm is the Gemini client.
//
// Task 3 owns this package. It implements contract.LLM over the Gemini
// REST API (generateContent) with net/http, so the module needs no SDK
// and its tests run against a local stand-in.
//
// All calls share one limit per minute (GEMINI_MAX_CALLS_PER_MINUTE).
// On HTTP 429 or 503 the client retries the primary model once, then
// tries the fallback model, then returns contract.ErrBusy.
//
// Tests against the real API sit behind the "gemini" build tag; see
// live_test.go.
package llm
