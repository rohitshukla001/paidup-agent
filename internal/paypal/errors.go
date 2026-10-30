package paypal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"paidup-agent/internal/contract"
)

// APIError is an error reply from PayPal. It wraps one of the shared
// errors in internal/contract, so callers can test with errors.Is.
type APIError struct {
	Status  int
	Name    string // for example INVALID_REQUEST
	Message string
	DebugID string // give this to PayPal support
	Details []ErrorDetail
	kind    error
}

// ErrorDetail is one field-level problem in an APIError.
type ErrorDetail struct {
	Field       string `json:"field"`
	Issue       string `json:"issue"`
	Description string `json:"description"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "paypal: %d %s", e.Status, e.Name)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	for _, d := range e.Details {
		fmt.Fprintf(&b, "; %s %s", d.Field, d.Issue)
		if d.Description != "" {
			fmt.Fprintf(&b, " (%s)", d.Description)
		}
	}
	if e.DebugID != "" {
		fmt.Fprintf(&b, " [debug_id %s]", e.DebugID)
	}
	return b.String()
}

// Unwrap returns the shared error that matches the HTTP status.
func (e *APIError) Unwrap() error { return e.kind }

func newAPIError(status int, body []byte) *APIError {
	var raw struct {
		Name    string        `json:"name"`
		Message string        `json:"message"`
		DebugID string        `json:"debug_id"`
		Details []ErrorDetail `json:"details"`
		// The OAuth endpoint uses a different shape.
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &raw)
	e := &APIError{
		Status:  status,
		Name:    raw.Name,
		Message: raw.Message,
		DebugID: raw.DebugID,
		Details: raw.Details,
		kind:    kindFor(status),
	}
	if e.Name == "" {
		e.Name = raw.Error
	}
	if e.Message == "" {
		e.Message = raw.ErrorDescription
	}
	if e.Name == "" {
		e.Name = http.StatusText(status)
	}
	return e
}

func kindFor(status int) error {
	switch {
	case status == http.StatusNotFound:
		return contract.ErrNotFound
	case status == http.StatusBadRequest,
		status == http.StatusUnprocessableEntity:
		return contract.ErrValidation
	case status == http.StatusTooManyRequests:
		return contract.ErrRateLimited
	default:
		return contract.ErrUpstream
	}
}
