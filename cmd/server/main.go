// Command server runs PaidUp.
//
// Task 9 owns this file. wire() builds the real PayPal client, Gemini
// client, and Postgres store from the environment. On a local machine a
// missing setting falls back to the in-memory fake, so the app starts with
// no credentials. On Render (RENDER is set) every setting is required.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"paidup-agent/internal/agent"
	"paidup-agent/internal/contract"
	"paidup-agent/internal/events"
	"paidup-agent/internal/fakes"
	"paidup-agent/internal/httpapi"
	"paidup-agent/internal/jobs"
	"paidup-agent/internal/llm"
	"paidup-agent/internal/paypal"
	"paidup-agent/internal/store/postgres"
	"paidup-agent/internal/webhook"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

// deps holds every dependency that main wires together.
type deps struct {
	LLM    contract.LLM
	PayPal contract.PayPal
	Store  contract.Store
	Agent  contract.Agent
	Bus    contract.Bus
	Fakes  []string // names of the parts that run on fakes
}

// wire builds the dependencies. strict (on Render) turns a missing
// setting into an error instead of a fake.
func wire(ctx context.Context, getenv func(string) string, strict bool) (deps, error) {
	var d deps
	var missing []string
	need := func(part string, vars ...string) bool {
		var absent []string
		for _, v := range vars {
			if getenv(v) == "" {
				absent = append(absent, v)
			}
		}
		if len(absent) == 0 {
			return true
		}
		missing = append(missing, absent...)
		d.Fakes = append(d.Fakes, part)
		return false
	}

	if need("PayPal", "PAYPAL_CLIENT_ID", "PAYPAL_CLIENT_SECRET") {
		d.PayPal = paypal.New(paypal.ConfigFromEnv(getenv))
	} else {
		d.PayPal = fakes.NewPayPal()
	}

	if need("Gemini", "GEMINI_API_KEY", "GEMINI_MODEL_PRIMARY") {
		l, err := llm.New(ctx, llm.ConfigFromEnv(getenv))
		if err != nil {
			return d, err
		}
		d.LLM = l
	} else {
		d.LLM = &fakes.LLM{}
	}

	if need("Postgres", "DATABASE_URL") {
		s, err := postgres.New(ctx, getenv("DATABASE_URL"))
		if err != nil {
			return d, err
		}
		d.Store = s
	} else {
		d.Store = fakes.NewStore()
	}

	need("login", "DEMO_PASSWORD", "SESSION_SECRET")
	// PAYPAL_WEBHOOK_ID is not required: it exists only after the first
	// deploy gives the app a URL. Until it is set, webhooks get a 500 and
	// the dashboard's refresh button still works.
	if getenv("PAYPAL_WEBHOOK_ID") == "" {
		slog.Warn("PAYPAL_WEBHOOK_ID is not set; PayPal webhooks will fail")
	}
	if strict && len(missing) > 0 {
		return d, fmt.Errorf("missing settings: %s", strings.Join(missing, ", "))
	}

	d.Bus = events.NewBus()
	d.Agent = agent.New(d.LLM, d.PayPal, d.Store)
	return d, nil
}

// newHandler returns every HTTP route.
func newHandler(d deps, getenv func(string) string, onRender bool) http.Handler {
	cfg := httpapi.ConfigFromEnv(getenv)
	cfg.WebDir = getenv("WEB_DIR")
	cfg.CookieSecure = onRender
	if cfg.SessionSecret == "" && !onRender {
		// Local runs without a .env still get a working login cookie.
		cfg.SessionSecret = "local-development-only"
	}
	return httpapi.New(cfg, d.Agent, d.Store, d.PayPal, d.Bus,
		webhook.New(d.PayPal, d.Store, d.Bus))
}

// run starts the server and blocks until ctx ends. If ready is not nil, run
// sends the listen address on it when the server accepts connections.
func run(ctx context.Context, getenv func(string) string, logOut io.Writer,
	ready chan<- string) error {
	log := slog.New(slog.NewJSONHandler(logOut, nil))
	slog.SetDefault(log)

	port := getenv("PORT") // Render sets PORT.
	if port == "" {
		port = "8080"
	}
	onRender := getenv("RENDER") != "" // Render sets RENDER=true.

	d, err := wire(ctx, getenv, onRender)
	if err != nil {
		return fmt.Errorf("wire dependencies: %w", err)
	}
	if len(d.Fakes) > 0 {
		log.Warn("running with in-memory fakes; set the variables in "+
			".env.example for the real services", "fakes", d.Fakes)
	}

	jobCtx, stopJob := context.WithCancel(ctx)
	defer stopJob()
	if getenv("REMINDER_JOB") != "off" {
		go jobs.NewReminderJob(d.PayPal, d.Store, d.LLM, d.Bus).Run(jobCtx)
	}

	// Cancelling baseCtx at shutdown ends every request context, so open
	// SSE streams close instead of holding the shutdown.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		Handler:           newHandler(d, getenv, onRender),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: SSE streams stay open for a long time.
		IdleTimeout: 120 * time.Second,
	}

	ln, err := net.Listen("tcp", net.JoinHostPort("", port))
	if err != nil {
		return fmt.Errorf("listen on port %s: %w", port, err)
	}
	log.Info("server started", "addr", ln.Addr().String())
	if ready != nil {
		ready <- ln.Addr().String()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	log.Info("server stopping")
	stopJob()
	cancelBase()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
