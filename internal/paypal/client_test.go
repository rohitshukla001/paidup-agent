package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// fakeAPI is a small PayPal stand-in that records each request.
type fakeAPI struct {
	t *testing.T

	mu         sync.Mutex
	tokenCalls int
	requests   []recorded
	// handle answers non-token requests. It returns status and body.
	handle func(r *http.Request, body []byte) (int, string)
	// expiresIn is the token lifetime in seconds.
	expiresIn int
}

type recorded struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.URL.Path == "/v1/oauth2/token" {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "id" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid_client",`+
				`"error_description":"Client Authentication failed"}`)
			return
		}
		if string(body) != "grant_type=client_credentials" {
			f.t.Errorf("token body = %q", body)
		}
		f.mu.Lock()
		f.tokenCalls++
		n := f.tokenCalls
		exp := f.expiresIn
		f.mu.Unlock()
		if exp == 0 {
			exp = 32400
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"tok`+strconv.Itoa(n)+
			`","expires_in":`+strconv.Itoa(exp)+`}`)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, recorded{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Header: r.Header.Clone(), Body: body,
	})
	f.mu.Unlock()
	status, out := http.StatusOK, "{}"
	if f.handle != nil {
		status, out = f.handle(r, body)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, out)
}

func (f *fakeAPI) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		f.t.Fatal("no request recorded")
	}
	return f.requests[len(f.requests)-1]
}

var testNow = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func newTestClient(t *testing.T, f *fakeAPI) (*Client, *httptest.Server) {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := NewClient(Config{
		ClientID: "id", ClientSecret: "secret", BaseURL: srv.URL,
		WebhookID: "WH-ID", Now: func() time.Time { return testNow },
	})
	return c, srv
}

const invoiceJSON = `{
  "id": "INV2-AAAA-BBBB",
  "status": "SENT",
  "detail": {
    "invoice_number": "0007",
    "currency_code": "USD",
    "payment_term": {"term_type": "DUE_ON_DATE_SPECIFIED",
      "due_date": "2026-11-18"},
    "metadata": {
      "last_update_time": "2026-11-11T10:05:00Z",
      "recipient_view_url": "https://www.sandbox.paypal.com/invoice/p/#INV2"
    }
  },
  "primary_recipients": [{"billing_info": {
    "name": {"given_name": "Jane", "surname": "Doe"},
    "email_address": "jane@example.com"}}],
  "amount": {"currency_code": "USD", "value": "450.00"}
}`

func TestTokenIsCachedAndRefreshedBeforeExpiry(t *testing.T) {
	f := &fakeAPI{expiresIn: 3600, handle: func(*http.Request, []byte) (int, string) {
		return 200, invoiceJSON
	}}
	c, _ := newTestClient(t, f)
	now := testNow
	c.now = func() time.Time { return now }
	ctx := context.Background()

	for range 3 {
		if _, err := c.Get(ctx, "INV2-AAAA-BBBB"); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1", f.tokenCalls)
	}
	if got := f.last().Header.Get("Authorization"); got != "Bearer tok1" {
		t.Fatalf("Authorization = %q", got)
	}

	now = now.Add(56 * time.Minute) // inside the 5-minute margin
	if _, err := c.Get(ctx, "INV2-AAAA-BBBB"); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 2 {
		t.Fatalf("token calls = %d, want 2 after refresh", f.tokenCalls)
	}
}

func TestUnauthorizedDropsTokenAndRetriesOnce(t *testing.T) {
	calls := 0
	f := &fakeAPI{handle: func(r *http.Request, _ []byte) (int, string) {
		calls++
		if r.Header.Get("Authorization") == "Bearer tok1" {
			return 401, `{"name":"AUTHENTICATION_FAILURE"}`
		}
		return 200, invoiceJSON
	}}
	c, _ := newTestClient(t, f)
	if _, err := c.Get(context.Background(), "INV2-AAAA-BBBB"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || f.tokenCalls != 2 {
		t.Fatalf("calls = %d, token calls = %d", calls, f.tokenCalls)
	}
}

func TestBadCredentials(t *testing.T) {
	f := &fakeAPI{}
	c, srv := newTestClient(t, f)
	c2 := NewClient(Config{ClientID: "id", ClientSecret: "wrong", BaseURL: srv.URL})
	_, err := c2.Get(context.Background(), "X")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Name != "invalid_client" {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, contract.ErrUpstream) {
		t.Fatalf("401 should wrap ErrUpstream, got %v", err)
	}
	_ = c
	if _, err := NewClient(Config{}).Get(context.Background(), "X"); err == nil ||
		!strings.Contains(err.Error(), "PAYPAL_CLIENT_ID") {
		t.Fatalf("missing credentials: err = %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{404, contract.ErrNotFound},
		{400, contract.ErrValidation},
		{422, contract.ErrValidation},
		{429, contract.ErrRateLimited},
		{500, contract.ErrUpstream},
		{503, contract.ErrUpstream},
	}
	for _, tt := range tests {
		f := &fakeAPI{handle: func(*http.Request, []byte) (int, string) {
			return tt.status, `{"name":"SOME_ERROR","message":"bad",` +
				`"debug_id":"dbg1","details":[{"field":"/detail/currency_code",` +
				`"issue":"INVALID_CURRENCY"}]}`
		}}
		c, _ := newTestClient(t, f)
		_, err := c.Get(context.Background(), "X")
		if !errors.Is(err, tt.want) {
			t.Errorf("status %d: err = %v, want %v", tt.status, err, tt.want)
		}
		if err != nil && !strings.Contains(err.Error(), "dbg1") {
			t.Errorf("status %d: error lacks debug_id: %v", tt.status, err)
		}
	}
}

func TestCreateDraft(t *testing.T) {
	f := &fakeAPI{handle: func(r *http.Request, _ []byte) (int, string) {
		switch r.URL.Path {
		case "/v2/invoicing/generate-next-invoice-number":
			return 200, `{"invoice_number":"0007"}`
		case "/v2/invoicing/invoices":
			return 201, strings.Replace(invoiceJSON, `"SENT"`, `"DRAFT"`, 1)
		}
		return 404, `{}`
	}}
	c, _ := newTestClient(t, f)
	c.cfg.InvoicerEmail = "me@example.com"

	d := domain.InvoiceDraft{
		ClientName: "Jane Doe", ClientEmail: "jane@example.com",
		Currency: "USD", DueDays: 7, Note: "Thanks",
		Items: []domain.LineItem{{Name: "Landing page", Quantity: 1.5,
			UnitPrice: "300.00"}},
	}
	inv, err := c.CreateDraft(context.Background(), d, "req-1-create")
	if err != nil {
		t.Fatal(err)
	}
	if inv.ID != "INV2-AAAA-BBBB" || inv.Status != "DRAFT" ||
		inv.Total != "450.00" || inv.ClientName != "Jane Doe" ||
		!inv.DueDate.Equal(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("invoice = %+v", inv)
	}

	req := f.last()
	if req.Header.Get("PayPal-Request-Id") != "req-1-create" {
		t.Errorf("PayPal-Request-Id = %q", req.Header.Get("PayPal-Request-Id"))
	}
	if req.Header.Get("Prefer") != "return=representation" {
		t.Errorf("Prefer = %q", req.Header.Get("Prefer"))
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	want := `{"detail":{"currency_code":"USD","invoice_date":"2026-11-11",` +
		`"invoice_number":"0007","note":"Thanks","payment_term":` +
		`{"due_date":"2026-11-18","term_type":"DUE_ON_DATE_SPECIFIED"}},` +
		`"invoicer":{"email_address":"me@example.com"},` +
		`"items":[{"name":"Landing page","quantity":"1.5","unit_amount":` +
		`{"currency_code":"USD","value":"300.00"},"unit_of_measure":"QUANTITY"}],` +
		`"primary_recipients":[{"billing_info":{"email_address":` +
		`"jane@example.com","name":{"given_name":"Jane","surname":"Doe"}}}]}`
	got, _ := json.Marshal(body)
	if string(got) != want {
		t.Errorf("create body:\n got %s\nwant %s", got, want)
	}
}

func TestCreateDraftFollowsLinkReply(t *testing.T) {
	f := &fakeAPI{handle: func(r *http.Request, _ []byte) (int, string) {
		switch {
		case r.URL.Path == "/v2/invoicing/generate-next-invoice-number":
			return 200, `{"invoice_number":"0007"}`
		case r.Method == "POST" && r.URL.Path == "/v2/invoicing/invoices":
			return 201, `{"rel":"self","method":"GET","href":` +
				`"https://api-m.sandbox.paypal.com/v2/invoicing/invoices/INV2-AAAA-BBBB"}`
		case r.Method == "GET" && r.URL.Path == "/v2/invoicing/invoices/INV2-AAAA-BBBB":
			return 200, invoiceJSON
		}
		return 404, `{}`
	}}
	c, _ := newTestClient(t, f)
	inv, err := c.CreateDraft(context.Background(), sampleDraft(), "r")
	if err != nil || inv.ID != "INV2-AAAA-BBBB" {
		t.Fatalf("inv = %+v, err = %v", inv, err)
	}
}

func TestCreateDraftRejectsInvalidDraft(t *testing.T) {
	f := &fakeAPI{}
	c, _ := newTestClient(t, f)
	d := sampleDraft()
	d.Currency = "INR"
	if _, err := c.CreateDraft(context.Background(), d, "r"); !errors.Is(err,
		contract.ErrValidation) {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != 0 {
		t.Fatal("invalid draft reached PayPal")
	}
}

func sampleDraft() domain.InvoiceDraft {
	return domain.InvoiceDraft{ClientName: "Jane", ClientEmail: "jane@example.com",
		Currency: "USD", DueDays: 7,
		Items: []domain.LineItem{{Name: "Page", Quantity: 1, UnitPrice: "450"}}}
}

func TestSendRemindCancel(t *testing.T) {
	f := &fakeAPI{handle: func(*http.Request, []byte) (int, string) {
		return 204, ""
	}}
	c, _ := newTestClient(t, f)
	ctx := context.Background()

	tests := []struct {
		name     string
		run      func() error
		path     string
		reqID    string
		wantBody string
	}{
		{"send", func() error { return c.Send(ctx, "INV2-A", "r-send") },
			"/v2/invoicing/invoices/INV2-A/send", "r-send",
			`{"send_to_invoicer":false,"send_to_recipient":true}`},
		{"remind", func() error {
			return c.Remind(ctx, "INV2-A", "Friendly reminder", "Due soon", "r-remind")
		}, "/v2/invoicing/invoices/INV2-A/remind", "r-remind",
			`{"subject":"Friendly reminder","note":"Due soon",` +
				`"send_to_invoicer":false,"send_to_recipient":true}`},
		{"cancel", func() error { return c.Cancel(ctx, "INV2-A", "Duplicate", "r-cancel") },
			"/v2/invoicing/invoices/INV2-A/cancel", "r-cancel",
			`{"subject":"Invoice cancelled","note":"Duplicate",` +
				`"send_to_invoicer":false,"send_to_recipient":true}`},
	}
	for _, tt := range tests {
		if err := tt.run(); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		req := f.last()
		if req.Method != "POST" || req.Path != tt.path ||
			req.Header.Get("PayPal-Request-Id") != tt.reqID ||
			string(req.Body) != tt.wantBody {
			t.Errorf("%s: got %s %s id=%q body=%s", tt.name, req.Method, req.Path,
				req.Header.Get("PayPal-Request-Id"), req.Body)
		}
	}
	if err := c.Send(ctx, "", "r"); !errors.Is(err, contract.ErrValidation) {
		t.Errorf("empty ID: err = %v", err)
	}
}

func TestListAndSearch(t *testing.T) {
	f := &fakeAPI{handle: func(*http.Request, []byte) (int, string) {
		return 200, `{"items":[` + invoiceJSON + `]}`
	}}
	c, _ := newTestClient(t, f)
	ctx := context.Background()

	invs, err := c.List(ctx, "", 10)
	if err != nil || len(invs) != 1 || invs[0].PayerViewURL == "" {
		t.Fatalf("List all: %+v, %v", invs, err)
	}
	if r := f.last(); r.Method != "GET" || r.Path != "/v2/invoicing/invoices" ||
		!strings.Contains(r.Query, "page_size=10") {
		t.Errorf("List all request: %s %s?%s", r.Method, r.Path, r.Query)
	}

	if _, err := c.List(ctx, "SENT", 500); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Method != "POST" || r.Path != "/v2/invoicing/search-invoices" ||
		!strings.Contains(r.Query, "page_size=100") ||
		string(r.Body) != `{"status":["SENT"]}` {
		t.Errorf("List SENT request: %s %s?%s %s", r.Method, r.Path, r.Query, r.Body)
	}

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	if _, err := c.Search(ctx, "jane@example.com", from, to); err != nil {
		t.Fatal(err)
	}
	want := `{"invoice_date_range":{"end":"2026-11-30","start":"2026-11-01"},` +
		`"recipient_email":"jane@example.com"}`
	if got := string(f.last().Body); got != want {
		t.Errorf("Search body = %s, want %s", got, want)
	}
}

func TestToDomainFallbacks(t *testing.T) {
	c := NewClient(Config{Now: func() time.Time { return testNow }})
	var p ppInvoice
	_ = json.Unmarshal([]byte(`{"id":"I","status":"DRAFT","detail":`+
		`{"currency_code":"EUR"},"primary_recipients":[{"billing_info":`+
		`{"name":{"full_name":"Acme Ltd"},"email_address":"a@b.co"}}]}`), &p)
	inv := c.toDomain(p)
	if inv.ClientName != "Acme Ltd" || inv.Currency != "EUR" ||
		!inv.UpdatedAt.Equal(testNow) || !inv.DueDate.IsZero() {
		t.Fatalf("inv = %+v", inv)
	}
}

func TestSplitName(t *testing.T) {
	for in, want := range map[string][2]string{
		"Jane":            {"Jane", ""},
		"Jane Doe":        {"Jane", "Doe"},
		"Mary Ann  Smith": {"Mary Ann", "Smith"},
		"":                {"", ""},
	} {
		g, s := splitName(in)
		if g != want[0] || s != want[1] {
			t.Errorf("splitName(%q) = %q, %q", in, g, s)
		}
	}
}

func webhookHeaders() http.Header {
	h := http.Header{}
	h.Set("PAYPAL-AUTH-ALGO", "SHA256withRSA")
	h.Set("PAYPAL-CERT-URL", "https://api.sandbox.paypal.com/v1/notifications/certs/C")
	h.Set("PAYPAL-TRANSMISSION-ID", "t-id")
	h.Set("PAYPAL-TRANSMISSION-SIG", "sig")
	h.Set("PAYPAL-TRANSMISSION-TIME", "2026-11-11T10:00:00Z")
	return h
}

func TestVerifyWebhookSendsRawBodyUnchanged(t *testing.T) {
	status := "SUCCESS"
	f := &fakeAPI{handle: func(*http.Request, []byte) (int, string) {
		return 200, `{"verification_status":"` + status + `"}`
	}}
	c, _ := newTestClient(t, f)
	raw := []byte("{\n  \"id\": \"WH-1\",\n  \"event_type\": \"INVOICING.INVOICE.PAID\"\n}")

	ok, err := c.VerifyWebhook(context.Background(), webhookHeaders(), raw)
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	body := f.last().Body
	if !strings.HasSuffix(string(body), `,"webhook_event":`+string(raw)+`}`) {
		t.Fatalf("event bytes changed: %s", body)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("request is not valid JSON: %v", err)
	}
	if string(parsed["webhook_id"]) != `"WH-ID"` {
		t.Errorf("webhook_id = %s", parsed["webhook_id"])
	}

	status = "FAILURE"
	if ok, _ := c.VerifyWebhook(context.Background(), webhookHeaders(), raw); ok {
		t.Error("FAILURE reply returned true")
	}
}

func TestVerifyWebhookShortCircuits(t *testing.T) {
	f := &fakeAPI{}
	c, _ := newTestClient(t, f)
	ctx := context.Background()

	h := webhookHeaders()
	h.Del("PAYPAL-TRANSMISSION-SIG")
	if ok, err := c.VerifyWebhook(ctx, h, []byte(`{}`)); ok || err != nil {
		t.Errorf("missing header: ok = %v, err = %v", ok, err)
	}
	if ok, err := c.VerifyWebhook(ctx, webhookHeaders(), []byte(`{bad`)); ok || err != nil {
		t.Errorf("bad JSON: ok = %v, err = %v", ok, err)
	}
	if len(f.requests) != 0 {
		t.Error("short-circuit cases reached PayPal")
	}
	c.cfg.WebhookID = ""
	if _, err := c.VerifyWebhook(ctx, webhookHeaders(), []byte(`{}`)); err == nil {
		t.Error("missing webhook ID: want error")
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{"PAYPAL_CLIENT_ID": "a", "PAYPAL_CLIENT_SECRET": "b",
		"PAYPAL_WEBHOOK_ID": "w", "PAYPAL_INVOICER_EMAIL": "me@example.com"}
	cfg := ConfigFromEnv(func(k string) string { return env[k] })
	c := NewClient(cfg)
	if c.base != SandboxBaseURL || cfg.ClientID != "a" || cfg.WebhookID != "w" ||
		cfg.InvoicerEmail != "me@example.com" {
		t.Fatalf("cfg = %+v, base = %s", cfg, c.base)
	}
}
