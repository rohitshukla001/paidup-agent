package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"paidup-agent/internal/contract"
)

// DefaultBaseURL is the Gemini API endpoint.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// Config holds the settings for New.
type Config struct {
	APIKey            string
	PrimaryModel      string // for example the current Flash model
	FallbackModel     string // for example the current Flash-Lite model
	MaxCallsPerMinute int    // shared by all calls; 0 means no limit

	BaseURL    string           // default DefaultBaseURL
	HTTPClient *http.Client     // default: 60-second timeout
	RetryDelay time.Duration    // wait before the retry on 429; default 2s
	Now        func() time.Time // default: time.Now
}

// ConfigFromEnv reads the GEMINI_* variables from .env.example.
func ConfigFromEnv(getenv func(string) string) Config {
	n, _ := strconv.Atoi(getenv("GEMINI_MAX_CALLS_PER_MINUTE"))
	return Config{
		APIKey:            getenv("GEMINI_API_KEY"),
		PrimaryModel:      getenv("GEMINI_MODEL_PRIMARY"),
		FallbackModel:     getenv("GEMINI_MODEL_FALLBACK"),
		MaxCallsPerMinute: n,
	}
}

// Client implements contract.LLM. It is safe for concurrent use.
type Client struct {
	cfg     Config
	base    string
	http    *http.Client
	now     func() time.Time
	limiter *limiter
	cache   *lru
}

var _ contract.LLM = (*Client)(nil)

// New checks the settings and returns a Gemini client. It makes no
// network call.
func New(_ context.Context, cfg Config) (contract.LLM, error) {
	return NewClient(cfg)
}

// NewClient is New with the concrete type, for tests and tools.
func NewClient(cfg Config) (*Client, error) {
	var missing []string
	if cfg.APIKey == "" {
		missing = append(missing, "GEMINI_API_KEY")
	}
	if cfg.PrimaryModel == "" {
		missing = append(missing, "GEMINI_MODEL_PRIMARY")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("llm: set %s", strings.Join(missing, " and "))
	}
	c := &Client{cfg: cfg, http: cfg.HTTPClient, now: cfg.Now}
	c.base = strings.TrimRight(cfg.BaseURL, "/")
	if c.base == "" {
		c.base = DefaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 60 * time.Second}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.cfg.RetryDelay == 0 {
		c.cfg.RetryDelay = 2 * time.Second
	}
	c.limiter = newLimiter(cfg.MaxCallsPerMinute, time.Minute, c.now)
	c.cache = newLRU(200)
	return c, nil
}

// Chat sends one request and returns the text and function calls.
func (c *Client) Chat(ctx context.Context,
	req contract.ChatRequest) (contract.ChatResponse, error) {
	body := generateRequest{Contents: toContents(req.History)}
	if req.System != "" {
		body.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}
	if len(req.Tools) > 0 {
		decls := make([]functionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, functionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  normalizeSchema(t.Params),
			})
		}
		body.Tools = []tool{{FunctionDeclarations: decls}}
	}
	resp, model, err := c.generate(ctx, body)
	if err != nil {
		return contract.ChatResponse{}, err
	}
	out := fromResponse(resp)
	out.Model = model
	return out, nil
}

// generate runs the retry and fallback policy:
//
//  1. primary model
//  2. on 429 or 503, wait RetryDelay and call the primary model again
//  3. on 429 or 503 again, call the fallback model one time
//  4. on 429 or 503 again, return contract.ErrBusy
//
// Every attempt takes one slot from the shared limit.
func (c *Client) generate(ctx context.Context,
	body generateRequest) (generateResponse, string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return generateResponse{}, "", err
	}
	plan := []string{c.cfg.PrimaryModel, c.cfg.PrimaryModel}
	if c.cfg.FallbackModel != "" {
		plan = append(plan, c.cfg.FallbackModel)
	}
	var lastErr error
	for i, model := range plan {
		if i == 1 {
			if err := sleep(ctx, c.cfg.RetryDelay); err != nil {
				return generateResponse{}, "", err
			}
		}
		if err := c.limiter.wait(ctx, 2*time.Second); err != nil {
			return generateResponse{}, "", err
		}
		resp, err := c.post(ctx, model, payload)
		if err == nil {
			return resp, model, nil
		}
		if !errors.Is(err, errRetryable) {
			return generateResponse{}, "", err
		}
		lastErr = err
	}
	return generateResponse{}, "", fmt.Errorf("%w: %v", contract.ErrBusy, lastErr)
}

// errRetryable marks 429 and 503 replies.
var errRetryable = errors.New("retryable")

func (c *Client) post(ctx context.Context, model string,
	payload []byte) (generateResponse, error) {
	u := c.base + "/models/" + url.PathEscape(model) + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u,
		bytes.NewReader(payload))
	if err != nil {
		return generateResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return generateResponse{}, fmt.Errorf("%w: gemini %s: %v",
			contract.ErrUpstream, model, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return generateResponse{}, fmt.Errorf("%w: gemini %s: read: %v",
			contract.ErrUpstream, model, err)
	}
	if resp.StatusCode != http.StatusOK {
		return generateResponse{}, apiError(model, resp.StatusCode, raw)
	}
	var out generateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return generateResponse{}, fmt.Errorf("%w: gemini %s: decode: %v",
			contract.ErrUpstream, model, err)
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return generateResponse{}, fmt.Errorf("%w: gemini blocked the "+
			"prompt: %s", contract.ErrValidation, out.PromptFeedback.BlockReason)
	}
	if len(out.Candidates) == 0 {
		return generateResponse{}, fmt.Errorf("%w: gemini %s: no candidates",
			contract.ErrUpstream, model)
	}
	return out, nil
}

func apiError(model string, status int, raw []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := fmt.Sprintf("gemini %s: %d %s: %s", model, status, e.Error.Status,
		e.Error.Message)
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return fmt.Errorf("%w: %s", errRetryable, msg)
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %s", contract.ErrValidation, msg)
	default:
		return fmt.Errorf("%w: %s", contract.ErrUpstream, msg)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
