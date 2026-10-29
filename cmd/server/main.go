// Command server runs PaidUp.
//
// Task 1 wrote this file with fakes for every dependency. Task 9 owns it now:
// when a task is complete, replace its fake in wire() with the constructor
// from Section 4.3 of docs/implementation-plan.md.
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
	"syscall"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/fakes"
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
}

// wire builds the dependencies. Every one is a fake until Task 9 swaps it.
func wire(_ context.Context, _ func(string) string) (deps, error) {
	return deps{
		LLM:    &fakes.LLM{},
		PayPal: fakes.NewPayPal(),
		Store:  fakes.NewStore(),
		Agent:  fakes.Agent{},
		Bus:    fakes.NewBus(),
	}, nil
}

// newHandler returns the HTTP routes. Task 9 replaces the body with httpapi.New
// when Task 6 is complete.
func newHandler(_ deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

// run starts the server and blocks until ctx ends. If ready is not nil, run
// sends the listen address on it when the server accepts connections.
func run(ctx context.Context, getenv func(string) string, logOut io.Writer, ready chan<- string) error {
	log := slog.New(slog.NewJSONHandler(logOut, nil))

	port := getenv("PORT") // Render sets PORT.
	if port == "" {
		port = "8080"
	}

	d, err := wire(ctx, getenv)
	if err != nil {
		return fmt.Errorf("wire dependencies: %w", err)
	}

	srv := &http.Server{
		Handler:           newHandler(d),
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
