package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"paidup-agent/internal/domain"
)

var allowedImageTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true,
}

// chat reads a multipart form with "text" and an optional "image", then
// streams the agent's events back as SSE.
func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, domain.CodeValidation,
				"The photo is too large. The limit is 5 MB.")
			return
		}
		writeError(w, http.StatusBadRequest, domain.CodeValidation,
			"Send a multipart form with a text field and an optional image.")
		return
	}
	defer r.MultipartForm.RemoveAll()

	text := r.FormValue("text")
	var image []byte
	mimeType := ""
	if file, hdr, err := r.FormFile("image"); err == nil {
		defer file.Close()
		if hdr.Size > s.cfg.MaxImageBytes {
			writeError(w, http.StatusRequestEntityTooLarge, domain.CodeValidation,
				"The photo is too large. The limit is 5 MB.")
			return
		}
		image, err = io.ReadAll(io.LimitReader(file, s.cfg.MaxImageBytes+1))
		if err != nil || int64(len(image)) > s.cfg.MaxImageBytes {
			writeError(w, http.StatusRequestEntityTooLarge, domain.CodeValidation,
				"The photo is too large. The limit is 5 MB.")
			return
		}
		// Trust the bytes, not the file name or the client's header.
		mimeType = http.DetectContentType(image)
		if !allowedImageTypes[mimeType] {
			writeError(w, http.StatusBadRequest, domain.CodeValidation,
				"Attach a JPEG, PNG, or WebP photo.")
			return
		}
	}
	if strings.TrimSpace(text) == "" && len(image) == 0 {
		writeError(w, http.StatusBadRequest, domain.CodeValidation,
			"Type a message or attach a photo.")
		return
	}

	sse, ok := startSSE(w)
	if !ok {
		return
	}
	if err := s.agent.HandleMessage(r.Context(), sessionID(r.Context()), text,
		image, mimeType, s.relay(sse)); err != nil && r.Context().Err() == nil {
		s.log.Error("httpapi: chat", "err", err)
	}
}

// approve runs the approval and streams the result as SSE.
func (s *server) approve(w http.ResponseWriter, r *http.Request) {
	sse, ok := startSSE(w)
	if !ok {
		return
	}
	if err := s.agent.Approve(r.Context(), sessionID(r.Context()),
		r.PathValue("id"), s.relay(sse)); err != nil && r.Context().Err() == nil {
		s.log.Error("httpapi: approve", "err", err)
	}
}

// relay sends each agent event to the request's stream. Cards and their
// results also go to the bus, so other open dashboards update.
func (s *server) relay(sse *sseWriter) func(domain.Event) {
	return func(e domain.Event) {
		sse.send(e)
		switch e.Type {
		case domain.EventActionProposed:
			s.bus.Publish(e)
		case domain.EventActionResult:
			s.bus.Publish(e)
			var r struct {
				Invoice *domain.Invoice `json:"invoice"`
			}
			if json.Unmarshal(e.Data, &r) == nil && r.Invoice != nil &&
				r.Invoice.ID != "" {
				if inv, err := domain.NewEvent(domain.EventInvoiceUpdated,
					r.Invoice); err == nil {
					s.bus.Publish(inv)
				}
			}
		}
	}
}

func (s *server) reject(w http.ResponseWriter, r *http.Request) {
	if err := s.agent.Reject(r.Context(), sessionID(r.Context()),
		r.PathValue("id")); err != nil {
		s.writeStoreError(w, "reject", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listActions returns this session's actions and the reminder job's. A
// PENDING list leaves out cards that have expired.
func (s *server) listActions(w http.ResponseWriter, r *http.Request) {
	status := domain.ActionStatus(strings.ToUpper(r.URL.Query().Get("status")))
	all, err := s.store.ListActions(r.Context(), status)
	if err != nil {
		s.writeStoreError(w, "list actions", err)
		return
	}
	sid := sessionID(r.Context())
	now := s.cfg.Now()
	out := []domain.PendingAction{}
	for _, a := range all {
		if a.SessionID != "" && a.SessionID != sid {
			continue
		}
		if a.Status == domain.ActionPending && a.Expired(now) {
			continue
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) listInvoices(w http.ResponseWriter, r *http.Request) {
	invs, err := s.store.ListInvoices(r.Context(), "")
	if err != nil {
		s.writeStoreError(w, "list invoices", err)
		return
	}
	writeJSON(w, http.StatusOK, invs)
}

// refreshInvoice gets the latest status from PayPal, for when a webhook
// is late.
func (s *server) refreshInvoice(w http.ResponseWriter, r *http.Request) {
	inv, err := s.pp.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, "refresh invoice", err)
		return
	}
	if inv.UpdatedAt.IsZero() {
		inv.UpdatedAt = time.Now().UTC()
	}
	if err := s.store.UpsertInvoice(r.Context(), inv); err != nil {
		s.writeStoreError(w, "cache invoice", err)
		return
	}
	if e, err := domain.NewEvent(domain.EventInvoiceUpdated, inv); err == nil {
		s.bus.Publish(e)
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *server) summary(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = domain.PeriodThisMonth
	}
	invs, err := s.store.ListInvoices(r.Context(), "")
	if err != nil {
		s.writeStoreError(w, "summary", err)
		return
	}
	sums, err := domain.Summarize(invs, period, s.cfg.Now())
	if err != nil {
		s.writeStoreError(w, "summary", err)
		return
	}
	writeJSON(w, http.StatusOK, sums)
}

// eventStream sends bus events to one dashboard until it disconnects.
func (s *server) eventStream(w http.ResponseWriter, r *http.Request) {
	sse, ok := startSSE(w)
	if !ok {
		return
	}
	ch, stop := s.bus.Subscribe()
	defer stop()
	sid := sessionID(r.Context())
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			sse.ping()
		case e, open := <-ch:
			if !open {
				return
			}
			if !visibleTo(e, sid) {
				continue
			}
			sse.send(e)
		}
	}
}

// visibleTo hides another session's cards. Invoices are shared: the demo
// has one merchant.
func visibleTo(e domain.Event, sid string) bool {
	if e.Type != domain.EventActionProposed {
		return true
	}
	var a struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(e.Data, &a) != nil {
		return false
	}
	return a.SessionID == "" || a.SessionID == sid
}
