package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"paidup-agent/internal/domain"
)

// heartbeat keeps idle SSE connections open through proxies.
const heartbeat = 25 * time.Second

// sseWriter writes Server-Sent Events in the format of Section 4.5:
//
//	event: <type>
//	data: <JSON>
//	(blank line)
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func startSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, domain.CodeInternal,
			"Streaming is not supported.")
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // stops proxy buffering
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) send(e domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The JSON from domain.NewEvent has no newlines, so one data line is
	// enough.
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", e.Type, e.Data)
	s.f.Flush()
}

func (s *sseWriter) ping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprint(s.w, ": ping\n\n")
	s.f.Flush()
}
