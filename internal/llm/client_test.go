package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"paidup-agent/internal/contract"
)

// fakeGemini records requests and answers from a script of replies.
type fakeGemini struct {
	mu      sync.Mutex
	paths   []string
	bodies  []generateRequest
	keys    []string
	replies []reply // one per call; the last one repeats
}

type reply struct {
	status int
	body   string
}

func (f *fakeGemini) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req generateRequest
	_ = json.Unmarshal(raw, &req)
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.bodies = append(f.bodies, req)
	f.keys = append(f.keys, r.Header.Get("x-goog-api-key"))
	i := len(f.paths) - 1
	if i >= len(f.replies) {
		i = len(f.replies) - 1
	}
	rp := f.replies[i]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rp.status)
	io.WriteString(w, rp.body)
}

func (f *fakeGemini) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

var testNow = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func newTestClient(t *testing.T, f *fakeGemini, mut ...func(*Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{
		APIKey: "key", PrimaryModel: "flash", FallbackModel: "flash-lite",
		BaseURL: srv.URL, RetryDelay: time.Millisecond,
		Now: func() time.Time { return testNow },
	}
	for _, m := range mut {
		m(&cfg)
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func textReply(s string) reply {
	b, _ := json.Marshal(s)
	return reply{200, `{"candidates":[{"content":{"role":"model","parts":` +
		`[{"text":` + string(b) + `}]},"finishReason":"STOP"}]}`}
}

const status429 = `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`

func TestNewRequiresKeyAndModel(t *testing.T) {
	_, err := NewClient(Config{})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY and "+
		"GEMINI_MODEL_PRIMARY") {
		t.Fatalf("err = %v", err)
	}
	env := map[string]string{"GEMINI_API_KEY": "k", "GEMINI_MODEL_PRIMARY": "p",
		"GEMINI_MODEL_FALLBACK": "f", "GEMINI_MAX_CALLS_PER_MINUTE": "8"}
	cfg := ConfigFromEnv(func(k string) string { return env[k] })
	if cfg.MaxCallsPerMinute != 8 || cfg.FallbackModel != "f" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if _, err := New(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func TestChatRequestShape(t *testing.T) {
	f := &fakeGemini{replies: []reply{textReply("hi")}}
	c := newTestClient(t, f)
	img := []byte{0x89, 'P', 'N', 'G'}

	_, err := c.Chat(context.Background(), contract.ChatRequest{
		System: "be brief",
		History: []contract.Turn{
			{Role: "user", Text: "bill john", Image: img, MIMEType: "image/png"},
			{Role: "model", Text: "Checking.", Calls: []contract.FuncCall{
				{Name: "list_invoices", Args: map[string]any{"status": "SENT"},
					Signature: []byte("c2ln")},
			}},
			{Role: "user", Results: []contract.FuncResult{
				{Name: "list_invoices", Output: map[string]any{"count": 0}},
			}},
			{Role: "user"}, // empty turn is dropped
		},
		Tools: []contract.ToolDecl{{
			Name: "list_invoices", Description: "List invoices",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{"type": "string"},
					"limit":  map[string]any{"type": "integer"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "/models/flash:generateContent" || f.keys[0] != "key" {
		t.Fatalf("path %q key %q", f.paths[0], f.keys[0])
	}
	req := f.bodies[0]
	if req.SystemInstruction == nil || req.SystemInstruction.Parts[0].Text != "be brief" {
		t.Errorf("system = %+v", req.SystemInstruction)
	}
	if len(req.Contents) != 3 {
		t.Fatalf("contents = %d, want 3", len(req.Contents))
	}
	u := req.Contents[0]
	if u.Role != "user" || u.Parts[0].Text != "bill john" ||
		u.Parts[1].InlineData.MIMEType != "image/png" ||
		u.Parts[1].InlineData.Data != base64.StdEncoding.EncodeToString(img) {
		t.Errorf("user turn = %+v", u)
	}
	m := req.Contents[1]
	if m.Role != "model" || m.Parts[1].FunctionCall.Name != "list_invoices" ||
		m.Parts[1].ThoughtSignature != "c2ln" {
		t.Errorf("model turn = %+v", m)
	}
	r := req.Contents[2]
	if r.Role != "user" || r.Parts[0].FunctionResponse.Name != "list_invoices" {
		t.Errorf("result turn = %+v", r)
	}
	params := req.Tools[0].FunctionDeclarations[0].Parameters
	props := params["properties"].(map[string]any)
	if params["type"] != "OBJECT" ||
		props["status"].(map[string]any)["type"] != "STRING" ||
		props["limit"].(map[string]any)["type"] != "INTEGER" {
		t.Errorf("schema not normalized: %+v", params)
	}
}

func TestChatResponseKeepsSignatureAndSkipsThoughts(t *testing.T) {
	f := &fakeGemini{replies: []reply{{200, `{"candidates":[{"content":{` +
		`"role":"model","parts":[` +
		`{"text":"planning...","thought":true},` +
		`{"text":"Here you go. "},{"text":"Two invoices."},` +
		`{"functionCall":{"name":"get_invoice","args":{"invoice_id":"INV2-1"}},` +
		`"thoughtSignature":"AbC_-="}]}}]}`}}}
	c := newTestClient(t, f)
	resp, err := c.Chat(context.Background(), contract.ChatRequest{
		History: []contract.Turn{{Role: "user", Text: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Here you go. Two invoices." || resp.Model != "flash" {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.Calls) != 1 || resp.Calls[0].Args["invoice_id"] != "INV2-1" ||
		string(resp.Calls[0].Signature) != "AbC_-=" {
		t.Fatalf("calls = %+v", resp.Calls)
	}

	// The signature goes back unchanged on the next request.
	_, _ = c.Chat(context.Background(), contract.ChatRequest{History: []contract.Turn{
		{Role: "model", Calls: resp.Calls}}})
	if got := f.bodies[1].Contents[0].Parts[0].ThoughtSignature; got != "AbC_-=" {
		t.Errorf("signature sent back = %q", got)
	}
}

func TestRetryThenFallback(t *testing.T) {
	tests := []struct {
		name      string
		replies   []reply
		wantErr   error
		wantCalls int
		wantPaths []string
		wantModel string
	}{
		{"primary ok", []reply{textReply("ok")}, nil, 1,
			[]string{"flash"}, "flash"},
		{"retry ok", []reply{{429, status429}, textReply("ok")}, nil, 2,
			[]string{"flash", "flash"}, "flash"},
		{"fallback ok", []reply{{429, status429}, {503, `{}`}, textReply("ok")},
			nil, 3, []string{"flash", "flash", "flash-lite"}, "flash-lite"},
		{"all busy", []reply{{429, status429}}, contract.ErrBusy, 3,
			[]string{"flash", "flash", "flash-lite"}, ""},
		{"bad request", []reply{{400, `{"error":{"message":"bad schema"}}`}},
			contract.ErrValidation, 1, []string{"flash"}, ""},
		{"server error", []reply{{500, `{}`}}, contract.ErrUpstream, 1,
			[]string{"flash"}, ""},
		{"blocked", []reply{{200, `{"promptFeedback":{"blockReason":"SAFETY"}}`}},
			contract.ErrValidation, 1, []string{"flash"}, ""},
		{"no candidates", []reply{{200, `{"candidates":[]}`}},
			contract.ErrUpstream, 1, []string{"flash"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeGemini{replies: tt.replies}
			c := newTestClient(t, f)
			resp, err := c.Chat(context.Background(), contract.ChatRequest{
				History: []contract.Turn{{Role: "user", Text: "x"}}})
			if tt.wantErr == nil && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if f.calls() != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", f.calls(), tt.wantCalls)
			}
			for i, m := range tt.wantPaths {
				if f.paths[i] != "/models/"+m+":generateContent" {
					t.Errorf("call %d path = %s", i, f.paths[i])
				}
			}
			if resp.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", resp.Model, tt.wantModel)
			}
		})
	}
}

func TestNoFallbackModelStopsAfterRetry(t *testing.T) {
	f := &fakeGemini{replies: []reply{{429, status429}}}
	c := newTestClient(t, f, func(c *Config) { c.FallbackModel = "" })
	_, err := c.Chat(context.Background(), contract.ChatRequest{
		History: []contract.Turn{{Role: "user", Text: "x"}}})
	if !errors.Is(err, contract.ErrBusy) || f.calls() != 2 {
		t.Fatalf("err = %v, calls = %d", err, f.calls())
	}
}

func TestGlobalLimit(t *testing.T) {
	f := &fakeGemini{replies: []reply{textReply("ok")}}
	c := newTestClient(t, f, func(c *Config) { c.MaxCallsPerMinute = 2 })
	ctx := context.Background()
	req := contract.ChatRequest{History: []contract.Turn{{Role: "user", Text: "x"}}}
	for range 2 {
		if _, err := c.Chat(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	// The clock is frozen, so the window never frees: ErrBusy at once.
	start := time.Now()
	if _, err := c.Chat(ctx, req); !errors.Is(err, contract.ErrBusy) {
		t.Fatalf("third call: err = %v", err)
	}
	if time.Since(start) > time.Second || f.calls() != 2 {
		t.Fatalf("limit did not stop the call early (calls = %d)", f.calls())
	}
}

func TestLimiterWaitsForAShortGap(t *testing.T) {
	l := newLimiter(1, 50*time.Millisecond, time.Now)
	ctx := context.Background()
	if err := l.wait(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := l.wait(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 30*time.Millisecond {
		t.Fatalf("second call did not wait (%s)", d)
	}
	if err := newLimiter(0, time.Minute, time.Now).wait(ctx, 0); err != nil {
		t.Fatalf("no limit: %v", err)
	}
}

const draftJSON = `{"client_name":"John","client_email":"john@example.com",` +
	`"currency":"usd","items":[{"name":"Landing page","quantity":1,` +
	`"unit_price":"450.00"}],"due_days":7,"note":""}`

func TestExtractInvoice(t *testing.T) {
	f := &fakeGemini{replies: []reply{textReply(draftJSON)}}
	c := newTestClient(t, f)
	ctx := context.Background()

	d, err := c.ExtractInvoice(ctx, "Bill John 450 USD", []byte("img"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if d.ClientEmail != "john@example.com" || d.Currency != "USD" ||
		d.Items[0].UnitPrice != "450.00" || d.DueDays != 7 {
		t.Fatalf("draft = %+v", d)
	}
	req := f.bodies[0]
	if req.GenerationConfig == nil ||
		req.GenerationConfig.ResponseMIMEType != "application/json" ||
		req.GenerationConfig.ResponseSchema["type"] != "OBJECT" {
		t.Errorf("generation config = %+v", req.GenerationConfig)
	}
	if !strings.Contains(req.SystemInstruction.Parts[0].Text, "2026-11-11") {
		t.Error("system prompt lacks today's date")
	}
	if len(req.Contents[0].Parts) != 2 ||
		req.Contents[0].Parts[1].InlineData.MIMEType != "image/jpeg" {
		t.Errorf("parts = %+v", req.Contents[0].Parts)
	}

	// Same input: served from the cache.
	if _, err := c.ExtractInvoice(ctx, "Bill John 450 USD", []byte("img"),
		"image/jpeg"); err != nil || f.calls() != 1 {
		t.Fatalf("cache miss: calls = %d, err = %v", f.calls(), err)
	}
}

func TestExtractInvoiceEdgeCases(t *testing.T) {
	ctx := context.Background()

	fenced := &fakeGemini{replies: []reply{textReply("```json\n" + draftJSON + "\n```")}}
	if d, err := newTestClient(t, fenced).ExtractInvoice(ctx, "x", nil,
		""); err != nil || d.ClientName != "John" {
		t.Fatalf("fenced: %+v, %v", d, err)
	}

	bad := &fakeGemini{replies: []reply{textReply("not json")}}
	if _, err := newTestClient(t, bad).ExtractInvoice(ctx, "x", nil,
		""); !errors.Is(err, contract.ErrUpstream) {
		t.Fatalf("bad JSON: err = %v", err)
	}

	none := &fakeGemini{replies: []reply{textReply(draftJSON)}}
	if _, err := newTestClient(t, none).ExtractInvoice(ctx, "  ", nil,
		""); !errors.Is(err, contract.ErrValidation) || none.calls() != 0 {
		t.Fatalf("empty input: err = %v", err)
	}
}

func TestLRUEvictsOldest(t *testing.T) {
	c := newLRU(2)
	c.put("a", 1)
	c.put("b", 2)
	c.get("a") // a is now newest
	c.put("c", 3)
	if _, ok := c.get("b"); ok {
		t.Error("b should be evicted")
	}
	if _, ok := c.get("a"); !ok || c.len() != 2 {
		t.Error("a should stay")
	}
	c.put("a", 9)
	if v, _ := c.get("a"); v != 9 {
		t.Errorf("update: a = %v", v)
	}
}
