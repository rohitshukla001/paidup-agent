# PaidUp

Get paid for freelance work without writing invoices by hand. Describe a job or photograph a quote, and a Gemini agent drafts the PayPal invoice. It tracks payment and reminds late clients, but sends nothing without your approval.

PaidUp is an entry for the PayPal AI Hackathon 2026. It runs against the PayPal sandbox only.

## Status

The base architecture is in place: the shared contracts, the domain rules, in-memory fakes of every dependency, and a server that answers `/healthz`. The real services come next. Each one is a separate task in [the implementation plan](docs/implementation-plan.md).

## Run it

You need Go 1.24 or later.

```sh
make check   # vet, tests with the race detector, gofmt
make run     # starts the server on :8080
curl localhost:8080/healthz
```

The Makefile sets `GOWORK=off`. A `go.work` file in a parent folder would otherwise change which modules the build uses.

## How the code is laid out

| Path | What it holds | Owner |
|---|---|---|
| `internal/contract` | The interfaces between packages | Task 1 |
| `internal/domain` | Shared types, validation, status buckets, totals | Task 1 |
| `internal/fakes` | In-memory versions of every interface, for tests and for the first server | Task 1 |
| `internal/paypal` | PayPal REST client | Task 2 |
| `internal/llm` | Gemini client | Task 3 |
| `internal/agent` | Chat loop, tools, approval flow | Task 4 |
| `internal/store/postgres`, `migrations` | Postgres store | Task 5 |
| `internal/httpapi`, `internal/events` | REST API, SSE, live events | Task 6 |
| `internal/webhook`, `internal/jobs` | PayPal webhooks, reminder job | Task 7 |
| `frontend` | Angular app | Task 8 |
| `cmd/server`, `Dockerfile`, `render.yaml` | Wiring and deployment | Task 9 |

A package codes against `internal/contract` and never imports another task's package. That rule is what lets the tasks run in parallel. If you need a contract to change, change Section 4 of the plan first.

## License

MIT. See [LICENSE](LICENSE).
