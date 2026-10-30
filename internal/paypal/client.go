package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"paidup-agent/internal/contract"
)

// SandboxBaseURL is the PayPal sandbox REST endpoint.
const SandboxBaseURL = "https://api-m.sandbox.paypal.com"

// tokenRefreshMargin is how long before expiry the client gets a new token.
const tokenRefreshMargin = 5 * time.Minute

// Config holds the settings for New.
type Config struct {
	ClientID     string
	ClientSecret string
	BaseURL      string // default SandboxBaseURL
	WebhookID    string // needed only by VerifyWebhook
	// InvoicerEmail is the merchant email shown on invoices. When empty,
	// PayPal uses the account that owns the REST app.
	InvoicerEmail string

	HTTPClient *http.Client     // default: 30-second timeout
	Now        func() time.Time // default: time.Now
}

// ConfigFromEnv reads the PAYPAL_* variables from .env.example.
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		ClientID:      getenv("PAYPAL_CLIENT_ID"),
		ClientSecret:  getenv("PAYPAL_CLIENT_SECRET"),
		BaseURL:       getenv("PAYPAL_BASE_URL"),
		WebhookID:     getenv("PAYPAL_WEBHOOK_ID"),
		InvoicerEmail: getenv("PAYPAL_INVOICER_EMAIL"),
	}
}

// Client implements contract.PayPal. It is safe for concurrent use.
type Client struct {
	cfg  Config
	base string
	http *http.Client
	now  func() time.Time

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

var _ contract.PayPal = (*Client)(nil)

// New returns a PayPal client. It does not call PayPal until the first
// request.
func New(cfg Config) contract.PayPal { return NewClient(cfg) }

// NewClient is New with the concrete type, for tests and tools.
func NewClient(cfg Config) *Client {
	c := &Client{cfg: cfg, http: cfg.HTTPClient, now: cfg.Now}
	c.base = strings.TrimRight(cfg.BaseURL, "/")
	if c.base == "" {
		c.base = SandboxBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// accessToken returns a cached token, or gets a new one when the cached
// token expires in less than tokenRefreshMargin.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.tokenExp.Add(-tokenRefreshMargin)) {
		return c.token, nil
	}
	if c.cfg.ClientID == "" || c.cfg.ClientSecret == "" {
		return "", errors.New("paypal: PAYPAL_CLIENT_ID and " +
			"PAYPAL_CLIENT_SECRET must be set")
	}

	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: get token: %v", contract.ErrUpstream, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", newAPIError(resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("%w: bad token reply", contract.ErrUpstream)
	}
	c.token = tok.AccessToken
	c.tokenExp = c.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *Client) dropToken(token string) {
	c.mu.Lock()
	if c.token == token {
		c.token = ""
	}
	c.mu.Unlock()
}

// call is one REST request. A nil in sends no body; a nil out ignores the
// reply body. A 401 drops the token and retries once.
type call struct {
	method    string
	path      string // may include a query string
	in        any
	rawIn     []byte // sent as is; takes the place of in
	out       any
	requestID string // PayPal-Request-Id, for write calls
	prefer    string // Prefer header
}

func (c *Client) do(ctx context.Context, cl call) error {
	payload := cl.rawIn
	if payload == nil && cl.in != nil {
		var err error
		if payload, err = json.Marshal(cl.in); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		status, body, err := c.send(ctx, cl, token, payload)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			c.dropToken(token)
			continue
		}
		if status < 200 || status > 299 {
			return newAPIError(status, body)
		}
		if cl.out != nil && len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, cl.out); err != nil {
				return fmt.Errorf("%w: decode %s %s: %v",
					contract.ErrUpstream, cl.method, cl.path, err)
			}
		}
		return nil
	}
}

func (c *Client) send(ctx context.Context, cl call, token string,
	payload []byte) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, c.base+cl.path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cl.requestID != "" {
		req.Header.Set("PayPal-Request-Id", cl.requestID)
	}
	if cl.prefer != "" {
		req.Header.Set("Prefer", cl.prefer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s %s: %v",
			contract.ErrUpstream, cl.method, cl.path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: read %s %s: %v",
			contract.ErrUpstream, cl.method, cl.path, err)
	}
	return resp.StatusCode, b, nil
}
