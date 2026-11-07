package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// Config holds the settings for New.
type Config struct {
	DemoPassword  string // the one login password for the demo
	SessionSecret string // signs the session cookie
	AdminToken    string // enables POST /api/demo/reset; "" disables it
	WebDir        string // built Angular files; default "web"
	CookieSecure  bool   // set on HTTPS hosts such as Render
	MaxImageBytes int64  // default 5 MB

	Now    func() time.Time
	Logger *slog.Logger
}

// ConfigFromEnv reads DEMO_PASSWORD, SESSION_SECRET, and ADMIN_TOKEN.
// The caller sets WebDir and CookieSecure.
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		DemoPassword:  getenv("DEMO_PASSWORD"),
		SessionSecret: getenv("SESSION_SECRET"),
		AdminToken:    getenv("ADMIN_TOKEN"),
	}
}

type server struct {
	cfg     Config
	agent   contract.Agent
	store   contract.Store
	pp      contract.PayPal
	bus     contract.Bus
	webhook http.Handler
	log     *slog.Logger
}

// New returns the HTTP handler for the whole app.
func New(cfg Config, a contract.Agent, s contract.Store, p contract.PayPal,
	b contract.Bus, webhook http.Handler) http.Handler {
	if cfg.WebDir == "" {
		cfg.WebDir = "web"
	}
	if cfg.MaxImageBytes <= 0 {
		cfg.MaxImageBytes = 5 << 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if webhook == nil {
		webhook = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	srv := &server{cfg: cfg, agent: a, store: s, pp: p, bus: b,
		webhook: webhook, log: cfg.Logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/login", srv.login)
	mux.Handle("POST /api/webhooks/paypal", srv.webhook)
	mux.HandleFunc("POST /api/demo/reset", srv.demoReset)

	auth := srv.requireSession
	mux.Handle("GET /api/session", auth(srv.session))
	mux.Handle("POST /api/chat", auth(srv.chat))
	mux.Handle("POST /api/actions/{id}/approve", auth(srv.approve))
	mux.Handle("POST /api/actions/{id}/reject", auth(srv.reject))
	mux.Handle("GET /api/actions", auth(srv.listActions))
	mux.Handle("GET /api/invoices", auth(srv.listInvoices))
	mux.Handle("POST /api/invoices/{id}/refresh", auth(srv.refreshInvoice))
	mux.Handle("GET /api/summary", auth(srv.summary))
	mux.Handle("GET /api/events/stream", auth(srv.eventStream))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, domain.CodeNotFound, "No such API route.")
	})
	mux.Handle("/", srv.static())

	return srv.logRequests(securityHeaders(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush keeps SSE working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" {
			return
		}
		s.log.Info("http", "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, domain.ErrorData{Code: code, Message: msg})
}

// writeStoreError maps shared errors to HTTP replies and hides internal
// details from the client.
func (s *server) writeStoreError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, contract.ErrNotFound):
		writeError(w, http.StatusNotFound, domain.CodeNotFound, "Not found.")
	case errors.Is(err, contract.ErrValidation):
		writeError(w, http.StatusBadRequest, domain.CodeValidation, err.Error())
	case errors.Is(err, contract.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, domain.CodeRateLimited,
			"PayPal is rate limiting requests. Try again soon.")
	default:
		s.log.Error("httpapi: "+what, "err", err)
		writeError(w, http.StatusInternalServerError, domain.CodeInternal,
			"Something went wrong. Try again.")
	}
}
