package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func startServer(t *testing.T, env map[string]string) (string, context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, io.Discard, ready) }()
	select {
	case addr := <-ready:
		return "http://" + addr, cancel, done
	case err := <-done:
		cancel()
		t.Fatalf("run ended early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not start")
	}
	return "", cancel, done
}

func TestLocalRunWithFakes(t *testing.T) {
	url, cancel, done := startServer(t, map[string]string{
		"PORT": "0", "DEMO_PASSWORD": "pw", "REMINDER_JOB": "off",
	})

	resp, err := http.Get(url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}

	resp, err = http.Post(url+"/api/login", "application/json",
		strings.NewReader(`{"password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || len(resp.Cookies()) != 1 {
		t.Fatalf("login = %d", resp.StatusCode)
	}

	// An open SSE stream must not hold up the shutdown.
	req, _ := http.NewRequest("GET", url+"/api/events/stream", nil)
	req.AddCookie(resp.Cookies()[0])
	stream, err := http.DefaultClient.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		t.Fatalf("stream: %v", err)
	}
	defer stream.Body.Close()

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("shutdown took %s", time.Since(start))
	}
}

func TestRenderNeedsEverySetting(t *testing.T) {
	env := map[string]string{"PORT": "0", "RENDER": "true",
		"PAYPAL_CLIENT_ID": "id", "PAYPAL_CLIENT_SECRET": "secret"}
	err := run(context.Background(), func(k string) string { return env[k] },
		io.Discard, nil)
	if err == nil {
		t.Fatal("want an error for missing settings")
	}
	for _, v := range []string{"GEMINI_API_KEY", "DATABASE_URL", "DEMO_PASSWORD",
		"SESSION_SECRET"} {
		if !strings.Contains(err.Error(), v) {
			t.Errorf("error does not name %s: %v", v, err)
		}
	}
	if strings.Contains(err.Error(), "PAYPAL_CLIENT_ID") {
		t.Errorf("error names a setting that is present: %v", err)
	}
}

func TestRenderNeedsALongDemoPassword(t *testing.T) {
	env := map[string]string{"PORT": "0", "RENDER": "true",
		"PAYPAL_CLIENT_ID": "id", "PAYPAL_CLIENT_SECRET": "secret",
		"GEMINI_API_KEY": "key", "GEMINI_MODEL_PRIMARY": "model",
		"DATABASE_URL": "postgres://unused", "SESSION_SECRET": "s",
		"DEMO_PASSWORD": "short"}
	_, err := wire(context.Background(), func(k string) string { return env[k] }, true)
	if err == nil || !strings.Contains(err.Error(), "DEMO_PASSWORD must have") {
		t.Fatalf("err = %v", err)
	}
}
