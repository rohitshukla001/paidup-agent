# Implementation plan: PaidUp

PaidUp is a chat agent that creates and sends PayPal invoices for freelancers after the user approves each action.

Repository: `paidup-agent`.

Deadline: 12 Nov 2026, 12:00 PM PST. In IST, this is 13 Nov 2026, 1:30 AM.

Target submission: 11 Nov 2026 (IST). Keep 12 Nov as a buffer day.

## 1. How the tasks stay independent

This plan has 10 tasks. Each task codes against the shared contracts in Section 4. No task codes against the code of a different task.

Task 1 supplies three shared parts:

1. The contract package `internal/contract`. Only Task 1 changes it.
2. The domain package `internal/domain`, with the shared types and rules.
3. In-memory fakes of each interface in `internal/fakes`. Tests and the first `main.go` use these fakes.

After Task 1, you can do Tasks 2 to 8 in any order, or in parallel sessions. Task 1 takes about half a day.

Two parts of the plan need the other tasks to be complete:

- Task 9, step 7 (integration) and step 8 (end-to-end test).
- Task 10, steps 4 and 5 (the video).

Before you start a task, read Sections 1 to 4. Then read the task card.

## 2. Independence from Vendasta

This project is personal. It has no connection to Vendasta. Obey these rules in each task:

- Use personal accounts only: GitHub, Google, PayPal Developer, Render, Devpost, and YouTube.
- Use a personal email address for each account. Do not use a vendasta.com address.
- Do not copy code, protos, templates, or libraries from Vendasta repositories, such as `galaxy` or `vendastaapis`.
- Do not use Vendasta CI, cloud projects, secrets, AI tools, or internal systems.
- Use a personal computer if possible.
- Do the work outside work hours.
- Do not build features for reviews or reputation management.

## 3. Product and architecture

The user describes a job or uploads a photo of a quote. Gemini proposes a PayPal action. The server validates the action, and the user approves it. Then the server executes it.

```
Angular app (chat, approval cards, dashboard)
      |  REST + Server-Sent Events (SSE)
      v
Go server on Render (it also serves the Angular build)
  agent    -> llm      -> Gemini API
           -> paypal   -> PayPal sandbox REST API
           -> store    -> Postgres
  webhook  <- PayPal webhooks
  jobs     -> overdue invoice scan
  events   -> live updates to the browser
```

### 3.1 Design decisions

1. **The Go code calls the PayPal REST API directly.** On about 6 Oct 2026, a forum post reported a `PAYPAL_API_SETUP_ERROR` from `create_invoice` on the sandbox MCP server. The plan does not depend on that server. The cost: the project does not show the PayPal Agent Toolkit. The AI use is still central, because Gemini selects and fills each PayPal call. If the MCP server is stable and time remains after 8 Nov, add it as an extra tool source.
2. **PayPal is the source of truth for invoices.** Postgres keeps a cache, so the dashboard loads fast and the summary needs no PayPal call. The cost: the cache can be out of date. Webhooks update it, and a "Refresh status" button gets the latest status on request.
3. **One Render web service serves the API and the Angular files.** Thus the project has one URL and one deployment.
4. **The model never executes a write action.** The server validates each proposal. Only an approval from the user starts a PayPal write call.

### 3.2 Demo script

Task 8, Task 9, and Task 10 use this script:

1. Log in with the demo password.
2. Type: "Bill Jane at jane@example.com 450 USD for a landing page, due in 7 days."
3. Approve the invoice card. The server creates the invoice and sends it.
4. Open the invoice link on the card.
5. Pay the invoice with the sandbox Personal account.
6. Look at the dashboard. The status changes to PAID.
7. Upload a sample photo from `testdata/samples`.
8. Ask: "Who owes me money?"

## 4. Shared contracts

This section is the contract. Task 1 commits it as Go code. If a task needs a change to a contract, change this section first. Then change `internal/contract`, and tell the other tasks that use it.

### 4.1 Domain types (`internal/domain`)

```go
type LineItem struct {
    Name      string  `json:"name"`
    Quantity  float64 `json:"quantity"`
    UnitPrice string  `json:"unit_price"` // decimal string, for example "450.00"
}

type InvoiceDraft struct {
    ClientName  string     `json:"client_name"`
    ClientEmail string     `json:"client_email"`
    Currency    string     `json:"currency"` // see AllowedCurrencies
    Items       []LineItem `json:"items"`
    DueDays     int        `json:"due_days"`
    Note        string     `json:"note"`
}

type Invoice struct {
    ID           string    `json:"id"`
    Number       string    `json:"number"`
    Status       string    `json:"status"` // PayPal status, see Bucket()
    ClientName   string    `json:"client_name"`
    ClientEmail  string    `json:"client_email"`
    Currency     string    `json:"currency"`
    Total        string    `json:"total"`
    DueDate      time.Time `json:"due_date"`
    PayerViewURL string    `json:"payer_view_url"`
    UpdatedAt    time.Time `json:"updated_at"`
}

type ActionStatus string // PENDING, APPROVED, REJECTED, EXPIRED, DONE, FAILED

type PendingAction struct {
    ID        string          `json:"id"`
    SessionID string          `json:"session_id"` // "" for actions that the job creates
    Tool      string          `json:"tool"`
    InvoiceID string          `json:"invoice_id"` // "" for propose_invoice
    Args      json.RawMessage `json:"args"`       // see Section 4.4
    Preview   json.RawMessage `json:"preview"`    // see Section 4.4
    Status    ActionStatus    `json:"status"`
    RequestID string          `json:"request_id"` // new UUID, set by the code that saves the action
    Result    json.RawMessage `json:"result"`
    ExpiresAt time.Time       `json:"expires_at"`
}

type Message struct {
    ID        string    `json:"id"`
    SessionID string    `json:"session_id"`
    Role      string    `json:"role"`      // user, model, tool
    Content   string    `json:"content"`   // text, or JSON for a function call or result
    ImageRef  string    `json:"image_ref"` // SHA-256 of the image; the server does not keep image bytes
    CreatedAt time.Time `json:"created_at"`
}

type Event struct {
    Type string          `json:"type"`
    Data json.RawMessage `json:"data"`
}

type Summary struct {
    Currency string `json:"currency"`
    Paid     string `json:"paid"`
    Unpaid   string `json:"unpaid"`
    Overdue  string `json:"overdue"`
}

var AllowedCurrencies = []string{"USD", "EUR", "GBP", "CAD", "AUD"}

func ValidateDraft(d InvoiceDraft) error                                     // rules in Section 4.6
func Bucket(inv Invoice, now time.Time) string                               // "paid", "unpaid", "overdue", or ""
func Summarize(invs []Invoice, period string, now time.Time) ([]Summary, error) // one Summary for each currency
func DraftTotalCents(d InvoiceDraft) (int64, error)
func ParseAmount(s string) (int64, error) // "450.05" -> 45005
func FormatAmount(cents int64) string     // 45005 -> "450.05"
func NewID() string                       // random UUID for action IDs and request IDs
```

### 4.2 Interfaces (`internal/contract`)

```go
type LLM interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ExtractInvoice(ctx context.Context, text string, image []byte, mimeType string) (domain.InvoiceDraft, error)
}
type ToolDecl struct { Name, Description string; Params map[string]any } // Params is a JSON schema
type FuncCall struct { Name string; Args map[string]any; Signature []byte } // Signature: Gemini thought signature, send it back unchanged
type FuncResult struct { Name string; Output map[string]any }
type Turn struct { Role string; Text string; Image []byte; MIMEType string; Calls []FuncCall; Results []FuncResult } // Role: "user" or "model"
type ChatRequest struct { System string; History []Turn; Tools []ToolDecl }
type ChatResponse struct { Text string; Calls []FuncCall; Model string }
// Shared errors. Implementations wrap them, so callers can test with errors.Is.
var (
    ErrNotFound    = errors.New("not found")
    ErrValidation  = domain.ErrValidation
    ErrRateLimited = errors.New("rate limited")
    ErrUpstream    = errors.New("upstream error")
    ErrBusy        = errors.New("llm: all models are rate limited")
)

type PayPal interface {
    CreateDraft(ctx context.Context, d domain.InvoiceDraft, requestID string) (domain.Invoice, error)
    Send(ctx context.Context, invoiceID, requestID string) error
    Remind(ctx context.Context, invoiceID, subject, note, requestID string) error
    Cancel(ctx context.Context, invoiceID, reason, requestID string) error
    Get(ctx context.Context, invoiceID string) (domain.Invoice, error)
    List(ctx context.Context, status string, limit int) ([]domain.Invoice, error)
    Search(ctx context.Context, email string, from, to time.Time) ([]domain.Invoice, error)
    VerifyWebhook(ctx context.Context, h http.Header, rawBody []byte) (bool, error)
}

type Store interface {
    CreateSession(ctx context.Context) (string, error)
    CountAIRequests(ctx context.Context, sessionID string, since time.Time) (int, error)
    RecordAIRequest(ctx context.Context, sessionID string) error
    AddMessage(ctx context.Context, m domain.Message) error
    RecentMessages(ctx context.Context, sessionID string, limit int) ([]domain.Message, error)
    SaveAction(ctx context.Context, a domain.PendingAction) error
    GetAction(ctx context.Context, id string) (domain.PendingAction, error)
    ListActions(ctx context.Context, status domain.ActionStatus) ([]domain.PendingAction, error) // "" for all
    TransitionAction(ctx context.Context, id string, from, to domain.ActionStatus) (bool, error) // atomic; false if status was not "from"
    SetActionResult(ctx context.Context, id string, to domain.ActionStatus, result json.RawMessage) error
    HasPendingAction(ctx context.Context, tool, invoiceID string) (bool, error)
    UpsertInvoice(ctx context.Context, inv domain.Invoice) error
    ListInvoices(ctx context.Context, status string) ([]domain.Invoice, error) // "" for all
    SaveEventOnce(ctx context.Context, eventID, eventType string, payload []byte) (bool, error) // false if duplicate
    AddReminder(ctx context.Context, invoiceID, note string) error
    LastReminder(ctx context.Context, invoiceID string) (time.Time, bool, error)
    Reset(ctx context.Context) error
}

type Agent interface {
    HandleMessage(ctx context.Context, sessionID, text string, image []byte, mimeType string, emit func(domain.Event)) error
    Approve(ctx context.Context, sessionID, actionID string, emit func(domain.Event)) error
    Reject(ctx context.Context, sessionID, actionID string) error
}

type Bus interface {
    Publish(e domain.Event)
    Subscribe() (<-chan domain.Event, func())
}
```

### 4.3 Constructors

`main.go` uses these functions. Each task exports the function for its own package.

| Task | Function |
|---|---|
| 2 | `paypal.New(cfg paypal.Config) contract.PayPal` |
| 3 | `llm.New(ctx context.Context, cfg llm.Config) (contract.LLM, error)` |
| 4 | `agent.New(l contract.LLM, p contract.PayPal, s contract.Store) contract.Agent` |
| 5 | `postgres.New(ctx context.Context, dsn string) (contract.Store, error)` |
| 6 | `events.NewBus() contract.Bus` |
| 6 | `httpapi.New(cfg httpapi.Config, a contract.Agent, s contract.Store, p contract.PayPal, b contract.Bus, webhook http.Handler) http.Handler` |
| 7 | `webhook.New(p contract.PayPal, s contract.Store, b contract.Bus) http.Handler` |
| 7 | `jobs.NewReminderJob(p contract.PayPal, s contract.Store, l contract.LLM, b contract.Bus) *jobs.ReminderJob` with `Run(ctx context.Context)` |

### 4.4 Tools

| Tool | Type | Args (JSON) | Preview (JSON) | PayPal calls on approval |
|---|---|---|---|---|
| `list_invoices` | Read | `{"status": "...", "limit": 10}` | None | `List` |
| `get_invoice` | Read | `{"invoice_id": "..."}` | None | `Get` |
| `search_invoices` | Read | `{"client_email": "...", "from_date": "2026-11-01", "to_date": "2026-11-30"}` | None | `Search` |
| `get_cash_summary` | Read | `{"period": "this_month"}` | None | None. It uses `domain.Summarize` on the cache. |
| `propose_invoice` | Write | An `InvoiceDraft` | `{"kind": "invoice", "draft": InvoiceDraft, "total": "450.00", "due_date": "2026-11-18"}` | `CreateDraft`, then `Send`, then `Get` |
| `propose_reminder` | Write | `{"invoice_id": "...", "subject": "...", "note": "..."}` | `{"kind": "reminder", "invoice": Invoice, "subject": "...", "note": "..."}` | `Remind` |
| `propose_cancel_invoice` | Write | `{"invoice_id": "...", "reason": "..."}` | `{"kind": "cancel", "invoice": Invoice, "reason": "..."}` | `Cancel` |

Rules for write tools:

- Each PayPal call in one approval gets its own idempotency key: `<RequestID>-create`, `<RequestID>-send`, `<RequestID>-remind`, or `<RequestID>-cancel`.
- An action that the chat creates expires after 15 minutes.
- An action that the reminder job creates expires after 72 hours.
- Any logged-in session can approve an action with an empty `SessionID`.
- After a successful `Remind`, the agent calls `AddReminder`.

Values for `period`: `this_month`, `last_month`, `last_30_days`, `all`.

### 4.5 Events and HTTP API

The server sends each SSE event in this format:

```
event: <type>
data: <JSON>

```

| Event type | Data |
|---|---|
| `message.delta` | `{"text": "..."}`. The agent sends one delta for each model reply. `Chat` does not stream tokens. |
| `message.done` | `{"message_id": "..."}` |
| `action.proposed` | A `PendingAction` with its `preview` |
| `action.result` | `{"action_id": "...", "status": "DONE", "invoice": Invoice}` |
| `invoice.updated` | An `Invoice` |
| `error` | `{"code": "...", "message": "..."}` |

Error codes: `rate_limited`, `busy`, `validation`, `expired`, `not_found`, `unauthorized`, `paypal_error`, `internal`.

| Method and path | Request | Reply |
|---|---|---|
| `POST /api/login` | `{"password": "..."}` | 204 and a signed session cookie |
| `POST /api/chat` | Multipart: field `text`, optional field `image` | SSE stream |
| `POST /api/actions/{id}/approve` | None | SSE stream |
| `POST /api/actions/{id}/reject` | None | 204 |
| `GET /api/actions?status=PENDING` | None | `[PendingAction]` |
| `GET /api/invoices` | None | `[Invoice]` |
| `POST /api/invoices/{id}/refresh` | None | `Invoice` |
| `GET /api/summary?period=this_month` | None | `[Summary]` |
| `GET /api/events/stream` | None | SSE stream of `invoice.updated` and `action.proposed` |
| `POST /api/webhooks/paypal` | PayPal event | 200 or 400 |
| `POST /api/demo/reset` | Header `X-Admin-Token` | 204 |
| `GET /healthz` | None | 200 |

Each error reply is JSON: `{"code": "...", "message": "..."}`.

### 4.6 Rules in `internal/domain`

`ValidateDraft` applies these rules:

- The client email address must have a valid format.
- Each item must have a name, a quantity more than 0, and a unit price more than 0.
- The invoice total must be more than 0 and not more than 10,000.
- The currency must be in `AllowedCurrencies`.
- `DueDays` must be from 1 to 90.

`Summarize` applies the period to paid invoices only, by `UpdatedAt`. The unpaid and overdue totals always show the current state.

`Bucket` puts each invoice in one bucket:

| PayPal status | Bucket |
|---|---|
| `PAID`, `MARKED_AS_PAID` | `paid` |
| `SENT`, `UNPAID`, `PARTIALLY_PAID`, `PAYMENT_PENDING`, with a due date today or later | `unpaid` |
| The same statuses, with a due date before today | `overdue` |
| All other statuses | `""` (not counted) |

### 4.7 Database tables

| Table | Columns |
|---|---|
| `sessions` | id, created_at |
| `ai_requests` | session_id, created_at |
| `messages` | id, session_id, role, content, image_ref, created_at |
| `pending_actions` | id, session_id, tool, invoice_id, args, preview, status, request_id, result, expires_at |
| `invoice_cache` | id, number, status, client_name, client_email, currency, total, due_date, payer_view_url, updated_at |
| `webhook_events` | event_id (unique), type, payload, received_at |
| `reminders` | id, invoice_id, note, sent_at |

A row in `reminders` records a reminder that PayPal sent.

### 4.8 Environment variables

- `PAYPAL_CLIENT_ID`, `PAYPAL_CLIENT_SECRET`, `PAYPAL_BASE_URL`, `PAYPAL_WEBHOOK_ID`
- `GEMINI_API_KEY`, `GEMINI_MODEL_PRIMARY`, `GEMINI_MODEL_FALLBACK`, `GEMINI_MAX_CALLS_PER_MINUTE`
- `DATABASE_URL`
- `DEMO_PASSWORD`, `ADMIN_TOKEN`, `SESSION_SECRET`

Put the names in `.env.example`. Do not put real values in the repository.

## 5. Tasks

### Task 1: Repository, contracts, and fakes

**Owns:** the repository root, `go.mod`, `internal/contract`, `internal/domain`, `internal/fakes`, `.github/workflows/go.yml`. Task 1 also writes the first `cmd/server/main.go`. After Task 1, Task 9 owns `main.go`.

**Steps:**

1. Create the public repository `paidup-agent` on your personal GitHub account. Select the MIT license.
2. Add the description and the topics.
3. Turn on secret scanning and push protection.
4. Commit Sections 4.1 to 4.4 as Go code.
5. Implement `ValidateDraft`, `Bucket`, and `Summarize`. Write table tests for them.
6. Write an in-memory fake for each interface in `internal/fakes`. The PayPal fake keeps invoices in a map and gives a fake payer link.
7. Add `.gitignore`. Put `.env` and `web/` in it.
8. Add `.env.example` with the names from Section 4.8.
9. Write `cmd/server/main.go`. It starts the server with the fakes and serves `/healthz`.
10. Add the workflow `go.yml`. It runs `go vet ./...` and `go test ./...`.

**Done when:**

- The license shows in the "About" section.
- `go build ./...` passes.
- The `go.yml` workflow passes.

### Task 2: PayPal client

**Owns:** `internal/paypal`.

**Steps:**

1. Log in to developer.paypal.com with your personal account.
2. Create a sandbox Business account. Use the US as the country.
3. Create a sandbox Personal account. Use the US as the country.
4. Create a REST app for the Business account. Make sure that the app has the Invoicing feature.
5. Implement the token cache. Get a new token 5 minutes before the old token expires.
6. Implement each method of `contract.PayPal` with the endpoints below.
7. Send a `PayPal-Request-Id` header with each write call.
8. Send `Prefer: return=representation` with the create call, so that PayPal returns the full invoice.
9. Wrap PayPal errors with the shared errors in `internal/contract`: `ErrNotFound`, `ErrValidation`, `ErrRateLimited`, `ErrUpstream`.
10. Write unit tests with `httptest`.
11. Write sandbox tests behind the build tag `sandbox`.

| Method | Endpoint | Notes |
|---|---|---|
| Token | `POST /v1/oauth2/token` | Client credentials. |
| CreateDraft | `POST /v2/invoicing/generate-next-invoice-number`, then `POST /v2/invoicing/invoices` | Set the invoicer email from the token owner. Calculate the due date from `DueDays`. |
| Send | `POST /v2/invoicing/invoices/{id}/send` | The payer link exists only after this call. |
| Remind | `POST /v2/invoicing/invoices/{id}/remind` | |
| Cancel | `POST /v2/invoicing/invoices/{id}/cancel` | |
| Get | `GET /v2/invoicing/invoices/{id}` | Read the payer link from `detail.metadata.recipient_view_url`. Confirm this field in the first sandbox reply. |
| List | `POST /v2/invoicing/search-invoices` | The GET list endpoint has no status filter. Use search with the `status` field. |
| Search | `POST /v2/invoicing/search-invoices` | |
| VerifyWebhook | `POST /v1/notifications/verify-webhook-signature` | Put the raw event body into the request without changes. A new JSON encoding of the event can break the check. |

**Done when:** `go test -tags sandbox ./internal/paypal` creates, sends, reminds, and lists a sandbox invoice.

### Task 3: Gemini client

**Owns:** `internal/llm`, `testdata/samples`.

**Steps:**

1. Create one Google Cloud project and one Gemini API key with your personal Google account.
2. Do not create more keys or more projects to get past the rate limits. This breaks the Google API terms.
3. In Google AI Studio, find the current Flash model and the current Flash-Lite model. Put their IDs in `.env.example` as the default values for `GEMINI_MODEL_PRIMARY` and `GEMINI_MODEL_FALLBACK`.
4. Implement `Chat` with the `google.golang.org/genai` SDK. Change each `ToolDecl` into a Gemini function declaration.
5. Return the thought signature of each function call in `FuncCall.Signature`. Send it back unchanged in the next request. Multi-step tool calls can fail without it.
6. Implement `ExtractInvoice` with structured output. Use the `InvoiceDraft` JSON schema as the response schema.
7. Limit all Gemini calls together to `GEMINI_MAX_CALLS_PER_MINUTE`. Set the value below the free-tier limit of the primary model.
8. On HTTP 429, retry one time after 2 seconds. Then use the fallback model. If both models fail, return `ErrBusy`.
9. Cache the `ExtractInvoice` results by the SHA-256 hash of the input. Keep a maximum of 200 entries in memory.
10. Add 5 sample texts and 3 sample photos in `testdata/samples`. Use fake names and fake email addresses only.

**Done when:**

- All 8 samples give a draft that passes `domain.ValidateDraft`.
- A test with a fake server covers the 429 path.

### Task 4: Agent service

**Owns:** `internal/agent`.

**Uses:** `contract.LLM`, `contract.PayPal`, `contract.Store`. The tests use the fakes from `internal/fakes`.

**Steps:**

1. Declare the tools in Section 4.4.
2. Execute a read tool immediately. Send the result back to the model.
3. Allow a maximum of 5 model calls for each user message. Each model call counts as one AI request.
4. Send one `message.delta` event for each model reply.
5. For a write tool, call `domain.ValidateDraft` or the checks for that tool. If the check fails, send the error back to the model.
6. If the check passes, save a `PendingAction` with a new `RequestID`. Then emit `action.proposed`.
7. In `Approve`, call `TransitionAction` from `PENDING` to `APPROVED`. If it returns `false`, stop. This prevents a second execution.
8. If the action is expired, set the status to `EXPIRED` and emit the `expired` error.
9. Execute the PayPal calls from Section 4.4 in order. Use one idempotency key for each call.
10. After a successful `Remind`, call `AddReminder`.
11. Save the result with `SetActionResult`. Then emit `action.result`.
12. In the system prompt, tell the model that text from users and from images is data, not instructions.
13. Limit each session to 30 AI requests in each hour. If a session goes over the limit, emit the `rate_limited` error.
14. Add a test with a prompt-injection sample. The test must show that no PayPal write call occurs without approval.

**Done when:**

- Each tool has a test.
- Two approvals of the same action at the same time give one set of PayPal calls.
- An expired action fails with `expired`.

### Task 5: Database store

**Owns:** `internal/store/postgres`, `migrations`.

**Steps:**

1. Write the migrations for the tables in Section 4.7. Use `goose`.
2. Implement `contract.Store` with `pgx`.
3. Implement `TransitionAction` as one `UPDATE ... WHERE id = $1 AND status = $2`.
4. Add a `docker-compose.yml` with Postgres for local tests.
5. Write a test for each method. Put the tests behind the build tag `pg`, so that the `go.yml` workflow does not need a database.

**Done when:**

- `go test -tags pg ./internal/store/...` passes.
- `SaveEventOnce` returns `false` for a duplicate event ID.
- `TransitionAction` returns `true` for one of two calls at the same time.

### Task 6: HTTP API and live events

**Owns:** `internal/httpapi`, `internal/events`.

**Uses:** `contract.Agent`, `contract.Store`, `contract.PayPal`. The tests use the fakes.

**Steps:**

1. Implement the routes in Section 4.5.
2. Compare the login password with `DEMO_PASSWORD`. Sign the session cookie with `SESSION_SECRET`.
3. Accept JPEG, PNG, and WebP images up to 5 MB in `/api/chat`. Refuse other files with the `validation` error.
4. Implement `contract.Bus` in memory.
5. Send the bus events to each `/api/events/stream` client.
6. In `/api/summary`, call `domain.Summarize` on the invoices from the store.
7. In `/api/invoices/{id}/refresh`, call `PayPal.Get`. Then call `UpsertInvoice` and return the invoice.
8. Accept `/api/demo/reset` only with the correct `ADMIN_TOKEN`.
9. Serve the files in `./web`. Send `index.html` for an unknown path that does not start with `/api/`.
10. Mount the `webhook` parameter of `httpapi.New` at `/api/webhooks/paypal`.

**Done when:**

- Each route has an `httptest` test.
- An SSE test receives an event from the bus.

### Task 7: Webhooks and the reminder job

**Owns:** `internal/webhook`, `internal/jobs`.

**Uses:** `contract.PayPal`, `contract.Store`, `contract.LLM`, `contract.Bus`. The tests use the fakes.

**Webhook steps:**

1. Limit the request body to 1 MB. Keep the raw bytes.
2. Verify the signature with `VerifyWebhook`. If the signature is not valid, return 400.
3. Save the event with `SaveEventOnce`. If the event is a duplicate, return 200 and do no more work.
4. For each event type below, get the invoice with `Get`.
5. Call `UpsertInvoice`.
6. Publish `invoice.updated`.

Event types: `INVOICING.INVOICE.PAID`, `INVOICING.INVOICE.UPDATED`, `INVOICING.INVOICE.CANCELLED`, `INVOICING.INVOICE.REFUNDED`.

Find the invoice ID in the event resource. Confirm the JSON path with the first real sandbox event, and write it in this section.

The webhook simulator in the PayPal dashboard sends mock events. A mock event can fail the signature check. Test the success path with a real sandbox payment.

**Reminder job steps:**

1. Run the job one time each hour.
2. Find the invoices in the `overdue` bucket.
3. Skip an invoice if PayPal sent a reminder less than 72 hours ago (`LastReminder`).
4. Skip an invoice if it has a pending `propose_reminder` action (`HasPendingAction`).
5. For each invoice that is not skipped, write a reminder note with `LLM.Chat`. These calls do not count against a session.
6. Save the note as a `propose_reminder` action with an empty `SessionID`.
7. Publish `action.proposed`.

**Done when:**

- A duplicate event has no effect.
- A bad signature gets 400.
- One overdue invoice gives a maximum of one open proposal at a time.

### Task 8: Angular frontend

**Owns:** `frontend`, `.github/workflows/frontend.yml`.

**Uses:** Sections 3.2 and 4.5 only. During development, use a mock backend: an HTTP interceptor with fixtures. An environment flag turns the mock on and off.

**Steps:**

1. Make a login page.
2. Make a chat page with text input, image upload, and the replies.
3. Read the SSE replies of `POST` endpoints with `fetch` and a stream reader. The browser `EventSource` API supports only `GET`.
4. Show each `action.proposed` event as an approval card with "Approve" and "Reject" buttons.
5. On an invoice card, show these fields:
   - The client
   - The items
   - The total
   - The due date
6. After approval, show the payer link from `action.result`.
7. Make a dashboard with the totals from `/api/summary` and the invoice table with status labels.
8. Add a "Refresh status" button to each invoice row.
9. Show pending actions from `/api/actions` and live events from `/api/events/stream`.
10. For the `rate_limited` and `busy` errors, show "The assistant is busy. Try again in one minute."
11. Make the pages work at a screen width of 360 px.
12. Make the pages work with the keyboard only.
13. Set the build output path to `web/` at the repository root.
14. Add the workflow `frontend.yml`. It runs the Angular build.

**Done when:** the demo script in Section 3.2 works from start to end with the mock backend.

### Task 9: Deployment and integration

**Owns:** `cmd/server/main.go` (after Task 1), `Dockerfile`, `render.yaml`, `scripts`.

**Steps:**

1. Create a personal Render account.
2. Claim the $50 in Render credits from the Render page of the hackathon.
3. Write a multi-stage `Dockerfile`. Stage 1 builds `frontend` if it exists. Stage 2 builds the Go binary. Stage 3 is a small runtime image.
4. Write `render.yaml` with one paid web service and one paid Postgres database. Use `/healthz` for the health check.
5. Do not use a free Render database. A free database expires after a fixed period, and the demo must work until judging ends.
6. Deploy the Task 1 skeleton now to get the public URL.
7. When a task is complete, replace its fake in `main.go` with the constructor from Section 4.3. Run the migrations at startup.
8. Write `scripts/e2e.sh`. It runs the demo script from Section 3.2 against the deployed URL.
9. In the PayPal app, add a webhook for `<URL>/api/webhooks/paypal`. Subscribe to the event types in Task 7. Set `PAYPAL_WEBHOOK_ID`.
10. Keep the service and the database on until 16 Dec 2026 (IST). Judging ends on 15 Dec 2026 at 8:00 AM PT, which is 9:30 PM IST.
11. Open the demo URL one time each week until judging ends.

**Done when:**

- `scripts/e2e.sh` passes against the deployed URL.
- After a sandbox payment, the dashboard shows PAID in less than 1 minute. If the webhook is late, one click on "Refresh status" shows PAID.

### Task 10: Submission package

**Owns:** `README.md`, `docs`, the Devpost form, the video.

**Steps:**

1. Write the README with these parts:
   - The key message
   - The features
   - The architecture diagram
   - The AI tools and how the project uses them
   - The local setup
   - The tests
2. Write the testing instructions for Devpost. Put the credentials of the sandbox Personal account in Devpost only, not in the repository.
3. Write the Devpost description with these parts:
   - The problem
   - The users
   - The features
   - The use of Gemini and PayPal
4. Record the video with the demo script from Section 3.2. It must be shorter than 3 minutes. Use music without copyright, or no music.
5. Upload the video to YouTube as a public video.
6. Complete the checklist below.
7. Submit the project on Devpost.

**Checklist:**

- [ ] The public repository has all the source code and the MIT license in "About".
- [ ] The hosted demo works in a new browser profile.
- [ ] The repository history has no secrets.
- [ ] The video is public and shorter than 3 minutes.
- [ ] All text is in English.

**Done when:** Devpost shows the project as submitted.

## 6. Suggested order

After Task 1, the tasks have no fixed order. This order fits the deadline:

| Dates (IST) | Tasks |
|---|---|
| 29 Oct to 30 Oct | Task 1. Task 9, steps 1 to 6. |
| 30 Oct to 4 Nov | Tasks 2, 3, 4, 5, and 6. Task 10, steps 1 to 3. |
| 5 Nov to 8 Nov | Tasks 7 and 8. Task 9, steps 7 and 9. |
| 9 Nov to 10 Nov | Task 9, step 8. Task 10, steps 4 to 6. |
| 11 Nov | Task 10, step 7: submit. |
| 12 Nov | Buffer. Fix problems only. |

If time is short, remove work in this order:

1. Remove the `propose_cancel_invoice` tool.
2. Remove the reminder job from Task 7. Keep the webhooks.

Do not remove the approval step, the payment status update, or the video.

## 7. Risks

| Risk | Effect | Action |
|---|---|---|
| Many judges use the demo at the same time. | Gemini returns 429, and the demo shows "busy". | The global limit in Task 3 keeps the call rate below the free-tier limit. The session limit stops one user from using all calls. If tests show "busy" often, add a small paid budget with a spending cap. |
| The PayPal sandbox has an outage. | A flow fails during the video recording. | Record the video before 11 Nov. Keep the buffer day. |
| A webhook arrives late. | The dashboard does not change. | Use the "Refresh status" button. |
| A contract changes after work starts. | Two tasks no longer fit together. | Change Section 4 first. Then tell each task that uses the contract. |
| A secret goes into the repository. | Someone can use the account. | Use `.gitignore`, `.env.example`, and secret scanning. |
