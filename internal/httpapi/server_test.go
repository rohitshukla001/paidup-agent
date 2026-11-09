package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
	"paidup-agent/internal/events"
	"paidup-agent/internal/fakes"
)

// stubAgent records calls and plays back events.
type stubAgent struct {
	mu        sync.Mutex
	lastText  string
	lastImage []byte
	lastMIME  string
	lastSID   string
	events    []domain.Event
	rejectErr error
}

func (a *stubAgent) HandleMessage(_ context.Context, sid, text string, image []byte,
	mime string, emit func(domain.Event)) error {
	a.mu.Lock()
	a.lastSID, a.lastText, a.lastImage, a.lastMIME = sid, text, image, mime
	evs := a.events
	a.mu.Unlock()
	for _, e := range evs {
		emit(e)
	}
	return nil
}

func (a *stubAgent) Approve(_ context.Context, sid, id string, emit func(domain.Event)) error {
	e, _ := domain.NewEvent(domain.EventActionResult, map[string]any{
		"action_id": id, "status": "DONE",
		"invoice": domain.Invoice{ID: "INV-9", Status: "SENT"}})
	emit(e)
	return nil
}

func (a *stubAgent) Reject(context.Context, string, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rejectErr
}

func (a *stubAgent) History(_ context.Context, sid string) ([]domain.ChatItem, error) {
	return []domain.ChatItem{{Role: "user", Text: "hi " + sid},
		{Role: "card", ActionID: "a1"}}, nil
}

func (a *stubAgent) setRejectErr(err error) {
	a.mu.Lock()
	a.rejectErr = err
	a.mu.Unlock()
}

// last returns what the last HandleMessage call received.
func (a *stubAgent) last() (sid, text, mime string, image []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastSID, a.lastText, a.lastMIME, a.lastImage
}

type testEnv struct {
	srv   *httptest.Server
	agent *stubAgent
	store *fakes.Store
	pp    *fakes.PayPal
	bus   contract.Bus
	now   time.Time
	hooks atomic.Int32
}

func newTestEnv(t *testing.T, mut ...func(*Config)) *testEnv {
	t.Helper()
	e := &testEnv{agent: &stubAgent{}, store: fakes.NewStore(),
		pp: fakes.NewPayPal(), bus: events.NewBus(),
		now: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	cfg := Config{DemoPassword: "pw", SessionSecret: "secret",
		AdminToken: "admin", WebDir: t.TempDir(),
		Now:    func() time.Time { return e.now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, m := range mut {
		m(&cfg)
	}
	hook := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		e.hooks.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	e.srv = httptest.NewServer(New(cfg, e.agent, e.store, e.pp, e.bus, hook))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *testEnv) do(t *testing.T, method, path, cookie string, body io.Reader,
	ctype string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *testEnv) login(t *testing.T) string {
	t.Helper()
	resp := e.do(t, "POST", "/api/login", "", strings.NewReader(`{"password":"pw"}`),
		"application/json")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	c := resp.Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Name != cookieName {
		t.Fatalf("cookie = %+v", c)
	}
	return c.Name + "=" + c.Value
}

func errCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var d domain.ErrorData
	_ = json.NewDecoder(resp.Body).Decode(&d)
	return d.Code
}

func TestLoginAndAuth(t *testing.T) {
	e := newTestEnv(t)
	if r := e.do(t, "POST", "/api/login", "", strings.NewReader(`{"password":"no"}`),
		""); r.StatusCode != 401 || errCode(t, r) != domain.CodeUnauthorized {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	if r := e.do(t, "POST", "/api/login", "", strings.NewReader(`not json`),
		""); r.StatusCode != 400 {
		t.Fatalf("bad body: %d", r.StatusCode)
	}
	cookie := e.login(t)

	if r := e.do(t, "GET", "/api/session", cookie, nil, ""); r.StatusCode != 204 {
		t.Fatalf("session with cookie: %d", r.StatusCode)
	}
	for _, c := range []string{"", cookieName + "=forged.sig",
		cookieName + "=" + strings.Replace(cookie[len(cookieName)+1:], "-", "x", 1) + "x"} {
		if r := e.do(t, "GET", "/api/invoices", c, nil, ""); r.StatusCode != 401 {
			t.Fatalf("cookie %q: status %d", c, r.StatusCode)
		}
	}
}

func TestLoginNotConfigured(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.DemoPassword = "" })
	if r := e.do(t, "POST", "/api/login", "", strings.NewReader(`{"password":""}`),
		""); r.StatusCode != 503 {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

// readSSE parses "event:" and "data:" lines until the stream ends or n
// events arrive.
func readSSE(t *testing.T, body io.Reader, n int) []domain.Event {
	t.Helper()
	var out []domain.Event
	sc := bufio.NewScanner(body)
	var cur domain.Event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = json.RawMessage(strings.TrimPrefix(line, "data: "))
		case line == "" && cur.Type != "":
			out = append(out, cur)
			cur = domain.Event{}
			if n > 0 && len(out) == n {
				return out
			}
		}
	}
	return out
}

func multipartBody(t *testing.T, text string, image []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("text", text)
	if image != nil {
		fw, _ := mw.CreateFormFile("image", "photo.bin")
		_, _ = fw.Write(image)
	}
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func TestChatStreamsAgentEvents(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.login(t)
	delta, _ := domain.NewEvent(domain.EventMessageDelta, map[string]string{"text": "Hi"})
	card, _ := domain.NewEvent(domain.EventActionProposed,
		domain.PendingAction{ID: "a1", SessionID: "x"})
	done, _ := domain.NewEvent(domain.EventMessageDone, map[string]string{"message_id": "m"})
	e.agent.events = []domain.Event{delta, card, done}

	sub, stop := e.bus.Subscribe()
	defer stop()

	body, ctype := multipartBody(t, "bill john", pngBytes)
	resp := e.do(t, "POST", "/api/chat", cookie, body, ctype)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	got := readSSE(t, resp.Body, 0)
	if len(got) != 3 || got[0].Type != domain.EventMessageDelta ||
		string(got[0].Data) != `{"text":"Hi"}` || got[2].Type != domain.EventMessageDone {
		t.Fatalf("events = %+v", got)
	}
	sid, text, mime, image := e.agent.last()
	if text != "bill john" || mime != "image/png" ||
		len(image) != len(pngBytes) || sid == "" {
		t.Fatalf("agent got text %q mime %q", text, mime)
	}
	select {
	case ev := <-sub:
		if ev.Type != domain.EventActionProposed {
			t.Fatalf("bus got %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("card was not published to the bus")
	}
}

func TestChatValidation(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.MaxImageBytes = 1000 })
	cookie := e.login(t)

	body, ctype := multipartBody(t, "x", []byte("GIF89a not allowed"))
	if r := e.do(t, "POST", "/api/chat", cookie, body, ctype); r.StatusCode != 400 ||
		errCode(t, r) != domain.CodeValidation {
		t.Fatalf("bad type: %d", r.StatusCode)
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 5000)...)
	body, ctype = multipartBody(t, "x", big)
	if r := e.do(t, "POST", "/api/chat", cookie, body, ctype); r.StatusCode != 413 {
		t.Fatalf("too big: %d", r.StatusCode)
	}
	body, ctype = multipartBody(t, "   ", nil)
	if r := e.do(t, "POST", "/api/chat", cookie, body, ctype); r.StatusCode != 400 {
		t.Fatalf("empty: %d", r.StatusCode)
	}
	if r := e.do(t, "POST", "/api/chat", cookie, strings.NewReader("{}"),
		"application/json"); r.StatusCode != 400 {
		t.Fatalf("not multipart: %d", r.StatusCode)
	}
}

func TestApproveAndReject(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.login(t)
	sub, stop := e.bus.Subscribe()
	defer stop()

	resp := e.do(t, "POST", "/api/actions/a1/approve", cookie, nil, "")
	got := readSSE(t, resp.Body, 0)
	if len(got) != 1 || got[0].Type != domain.EventActionResult ||
		!strings.Contains(string(got[0].Data), `"action_id":"a1"`) {
		t.Fatalf("events = %+v", got)
	}
	var types []string
	for range 2 {
		select {
		case ev := <-sub:
			types = append(types, ev.Type)
		case <-time.After(time.Second):
			t.Fatalf("bus got %v", types)
		}
	}
	if types[0] != domain.EventActionResult || types[1] != domain.EventInvoiceUpdated {
		t.Fatalf("bus types = %v", types)
	}

	if r := e.do(t, "POST", "/api/actions/a1/reject", cookie, nil, ""); r.StatusCode != 204 {
		t.Fatalf("reject: %d", r.StatusCode)
	}
	e.agent.setRejectErr(fmt.Errorf("%w: gone", contract.ErrNotFound))
	if r := e.do(t, "POST", "/api/actions/a1/reject", cookie, nil, ""); r.StatusCode != 404 {
		t.Fatalf("reject missing: %d", r.StatusCode)
	}
	e.agent.setRejectErr(fmt.Errorf("%w: already handled", contract.ErrValidation))
	if r := e.do(t, "POST", "/api/actions/a1/reject", cookie, nil, ""); r.StatusCode != 400 {
		t.Fatalf("reject handled: %d", r.StatusCode)
	}
}

func TestListActionsFiltersSessionAndExpiry(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.login(t)
	ctx := context.Background()
	// A chat request tells the stub agent this session's ID.
	body, ctype := multipartBody(t, "hi", nil)
	readAll(e.do(t, "POST", "/api/chat", cookie, body, ctype))
	sid, _, _, _ := e.agent.last()
	if sid == "" {
		t.Fatal("no session ID")
	}
	exp := e.now.Add(time.Hour)
	for _, a := range []domain.PendingAction{
		{ID: "mine", SessionID: sid, Status: domain.ActionPending, ExpiresAt: exp},
		{ID: "job", SessionID: "", Status: domain.ActionPending, ExpiresAt: exp},
		{ID: "other", SessionID: "someone-else", Status: domain.ActionPending, ExpiresAt: exp},
		{ID: "old", SessionID: sid, Status: domain.ActionPending, ExpiresAt: e.now.Add(-time.Minute)},
		{ID: "done", SessionID: sid, Status: domain.ActionDone, ExpiresAt: exp},
	} {
		_ = e.store.SaveAction(ctx, a)
	}
	r := e.do(t, "GET", "/api/actions?status=pending", cookie, nil, "")
	var got []domain.PendingAction
	_ = json.NewDecoder(r.Body).Decode(&got)
	ids := map[string]bool{}
	for _, a := range got {
		ids[a.ID] = true
	}
	if len(got) != 2 || !ids["mine"] || !ids["job"] {
		t.Fatalf("pending = %v", ids)
	}
}

func TestInvoicesSummaryRefresh(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.login(t)
	ctx := context.Background()

	inv, _ := e.pp.CreateDraft(ctx, fakes.SampleDraft(), "r1")
	_ = e.pp.Send(ctx, inv.ID, "r2")
	sub, stop := e.bus.Subscribe()
	defer stop()

	r := e.do(t, "POST", "/api/invoices/"+inv.ID+"/refresh", cookie, nil, "")
	var refreshed domain.Invoice
	_ = json.NewDecoder(r.Body).Decode(&refreshed)
	if r.StatusCode != 200 || refreshed.Status != "SENT" {
		t.Fatalf("refresh: %d %+v", r.StatusCode, refreshed)
	}
	select {
	case ev := <-sub:
		if ev.Type != domain.EventInvoiceUpdated {
			t.Fatalf("bus got %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not publish")
	}
	if r := e.do(t, "POST", "/api/invoices/nope/refresh", cookie, nil, ""); r.StatusCode != 404 {
		t.Fatalf("refresh missing: %d", r.StatusCode)
	}

	r = e.do(t, "GET", "/api/invoices", cookie, nil, "")
	var list []domain.Invoice
	_ = json.NewDecoder(r.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != inv.ID {
		t.Fatalf("invoices = %+v", list)
	}

	r = e.do(t, "GET", "/api/summary", cookie, nil, "")
	var sums []domain.Summary
	_ = json.NewDecoder(r.Body).Decode(&sums)
	if r.StatusCode != 200 || len(sums) != 1 || sums[0].Unpaid != "450.00" {
		t.Fatalf("summary = %d %+v", r.StatusCode, sums)
	}
	if r := e.do(t, "GET", "/api/summary?period=yesterday", cookie, nil, ""); r.StatusCode != 400 {
		t.Fatalf("bad period: %d", r.StatusCode)
	}
}

func TestEventStream(t *testing.T) {
	e := newTestEnv(t)
	cookie := e.login(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/api/events/stream", nil)
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Wait until the stream has subscribed.
	for i := 0; e.bus.(*events.Bus).Subscribers() == 0 && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	other, _ := domain.NewEvent(domain.EventActionProposed,
		domain.PendingAction{ID: "x", SessionID: "someone-else"})
	inv, _ := domain.NewEvent(domain.EventInvoiceUpdated, domain.Invoice{ID: "INV-1"})
	e.bus.Publish(other)
	e.bus.Publish(inv)

	got := readSSE(t, resp.Body, 1)
	if len(got) != 1 || got[0].Type != domain.EventInvoiceUpdated {
		t.Fatalf("events = %+v", got)
	}
	cancel()
	for i := 0; e.bus.(*events.Bus).Subscribers() != 0 && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := e.bus.(*events.Bus).Subscribers(); n != 0 {
		t.Fatalf("subscription leaked: %d", n)
	}
}

func TestWebhookResetHealthAndUnknown(t *testing.T) {
	e := newTestEnv(t)
	if r := e.do(t, "POST", "/api/webhooks/paypal", "", strings.NewReader("{}"),
		""); r.StatusCode != 200 || e.hooks.Load() != 1 {
		t.Fatalf("webhook: %d, hooks %d", r.StatusCode, e.hooks.Load())
	}

	_ = e.store.UpsertInvoice(context.Background(), domain.Invoice{ID: "I"})
	if r := e.do(t, "POST", "/api/demo/reset", "", nil, ""); r.StatusCode != 401 {
		t.Fatalf("reset without token: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/demo/reset", nil)
	req.Header.Set("X-Admin-Token", "admin")
	r, _ := http.DefaultClient.Do(req)
	r.Body.Close()
	if invs, _ := e.store.ListInvoices(context.Background(), ""); r.StatusCode != 204 ||
		len(invs) != 0 {
		t.Fatalf("reset: %d, %d invoices", r.StatusCode, len(invs))
	}

	r = e.do(t, "GET", "/healthz", "", nil, "")
	if b, _ := io.ReadAll(r.Body); r.StatusCode != 200 || string(b) != "ok\n" ||
		r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("healthz: %d %q", r.StatusCode, b)
	}
	if r := e.do(t, "GET", "/api/nope", "", nil, ""); r.StatusCode != 404 ||
		errCode(t, r) != domain.CodeNotFound {
		t.Fatalf("unknown route: %d", r.StatusCode)
	}

	off := newTestEnv(t, func(c *Config) { c.AdminToken = "" })
	if r := off.do(t, "POST", "/api/demo/reset", "", nil, ""); r.StatusCode != 404 {
		t.Fatalf("reset disabled: %d", r.StatusCode)
	}
}

func TestStaticFiles(t *testing.T) {
	dir := t.TempDir()
	e := newTestEnv(t, func(c *Config) { c.WebDir = dir })
	if r := e.do(t, "GET", "/", "", nil, ""); !strings.Contains(readAll(r), "not built") {
		t.Fatal("no build message missing")
	}

	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<app-root>"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "main-ABC123.js"), []byte("js"), 0o644)
	for _, p := range []string{"/", "/dashboard", "/chat/123"} {
		r := e.do(t, "GET", p, "", nil, "")
		if b := readAll(r); b != "<app-root>" || r.Header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %q", p, b)
		}
	}
	r := e.do(t, "GET", "/main-ABC123.js", "", nil, "")
	if readAll(r) != "js" || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("bundle: %q", r.Header.Get("Cache-Control"))
	}
	if r := e.do(t, "GET", "/../../etc/passwd", "", nil, ""); readAll(r) != "<app-root>" {
		t.Fatal("path escape was not contained")
	}
	if r := e.do(t, "POST", "/", "", nil, ""); r.StatusCode != 405 {
		t.Fatalf("POST /: %d", r.StatusCode)
	}
}

func readAll(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestMessagesReturnsThisSessionsChat(t *testing.T) {
	e := newTestEnv(t)
	if r := e.do(t, "GET", "/api/messages", "", nil, ""); r.StatusCode != 401 {
		t.Fatalf("no cookie: %d", r.StatusCode)
	}
	cookie := e.login(t)
	r := e.do(t, "GET", "/api/messages", cookie, nil, "")
	var items []domain.ChatItem
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil || r.StatusCode != 200 {
		t.Fatalf("status %d, err %v", r.StatusCode, err)
	}
	// The stub echoes the session ID, so a non-empty ID proves the cookie
	// reached the agent.
	if len(items) != 2 || len(items[0].Text) <= len("hi ") ||
		items[1].ActionID != "a1" {
		t.Fatalf("items = %+v", items)
	}
}
