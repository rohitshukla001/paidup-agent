package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestRunServesHealthzAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	env := map[string]string{"PORT": "0"}
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, io.Discard, ready) }()

	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("run ended early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}
