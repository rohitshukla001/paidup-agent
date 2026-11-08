package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// maxBody is the largest webhook body the handler reads.
const maxBody = 1 << 20

// invoiceEvents are the event types that change an invoice.
var invoiceEvents = map[string]bool{
	"INVOICING.INVOICE.PAID":      true,
	"INVOICING.INVOICE.UPDATED":   true,
	"INVOICING.INVOICE.CANCELLED": true,
	"INVOICING.INVOICE.REFUNDED":  true,
}

// Handler implements the PayPal webhook endpoint.
type Handler struct {
	pp    contract.PayPal
	store contract.Store
	bus   contract.Bus
	log   *slog.Logger
}

// New returns the webhook handler.
func New(p contract.PayPal, s contract.Store, b contract.Bus) http.Handler {
	return &Handler{pp: p, store: s, bus: b, log: slog.Default()}
}

type event struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	Resource  json.RawMessage `json:"resource"`
}

// ServeHTTP replies 200 to every verified event, also when processing
// fails after the event is saved: a PayPal retry would only be a
// duplicate, and the dashboard has a refresh button.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	ok, err := h.pp.VerifyWebhook(r.Context(), r.Header, raw)
	if err != nil {
		// PayPal could not check the signature now; a 500 makes it retry.
		h.log.Error("webhook: verify", "err", err)
		http.Error(w, "cannot verify", http.StatusInternalServerError)
		return
	}
	if !ok {
		h.log.Warn("webhook: bad signature")
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}

	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil || ev.ID == "" {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	first, err := h.store.SaveEventOnce(r.Context(), ev.ID, ev.EventType, raw)
	if err != nil {
		h.log.Error("webhook: save event", "err", err)
		http.Error(w, "cannot save", http.StatusInternalServerError)
		return
	}
	if !first {
		w.WriteHeader(http.StatusOK)
		return
	}
	if invoiceEvents[ev.EventType] {
		h.refresh(r.Context(), ev)
	}
	w.WriteHeader(http.StatusOK)
}

// refresh gets the invoice named in the event from PayPal. The event body
// is not trusted for the status: PayPal's API has the current truth.
func (h *Handler) refresh(ctx context.Context, ev event) {
	id := InvoiceID(ev.Resource)
	if id == "" {
		h.log.Warn("webhook: no invoice ID", "event", ev.ID, "type", ev.EventType)
		return
	}
	inv, err := h.pp.Get(ctx, id)
	if err != nil {
		h.log.Error("webhook: get invoice", "id", id, "err", err)
		return
	}
	if inv.UpdatedAt.IsZero() {
		inv.UpdatedAt = time.Now().UTC()
	}
	if err := h.store.UpsertInvoice(ctx, inv); err != nil {
		h.log.Error("webhook: cache invoice", "id", id, "err", err)
		return
	}
	if e, err := domain.NewEvent(domain.EventInvoiceUpdated, inv); err == nil {
		h.bus.Publish(e)
	}
}

var invoiceIDPattern = regexp.MustCompile(`INV2(?:-[A-Z0-9]{4}){4}`)

// InvoiceID finds the invoice ID in an event resource. PayPal sends
// resource.invoice.id for invoicing events; resource.id and an ID inside
// a link are fallbacks.
func InvoiceID(resource json.RawMessage) string {
	var r struct {
		ID      string `json:"id"`
		Invoice struct {
			ID string `json:"id"`
		} `json:"invoice"`
	}
	if json.Unmarshal(resource, &r) == nil {
		if r.Invoice.ID != "" {
			return r.Invoice.ID
		}
		if invoiceIDPattern.MatchString(r.ID) {
			return r.ID
		}
	}
	return invoiceIDPattern.FindString(string(resource))
}
