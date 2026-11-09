package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// Read tool names. Write tool names live in the domain package.
const (
	toolListInvoices   = "list_invoices"
	toolGetInvoice     = "get_invoice"
	toolSearchInvoices = "search_invoices"
	toolCashSummary    = "get_cash_summary"
)

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// toolDecls are the tools in Section 4.4 of the plan.
var toolDecls = []contract.ToolDecl{
	{Name: toolListInvoices,
		Description: "List the freelancer's PayPal invoices, newest first.",
		Params: obj(map[string]any{
			"status": str("PayPal status filter: DRAFT, SENT, UNPAID, " +
				"PAID, CANCELLED. Empty for all."),
			"limit": map[string]any{"type": "integer",
				"description": "Maximum invoices, 1 to 50. Default 10."},
		})},
	{Name: toolGetInvoice, Description: "Get one invoice by its PayPal ID.",
		Params: obj(map[string]any{"invoice_id": str("PayPal invoice ID")},
			"invoice_id")},
	{Name: toolSearchInvoices,
		Description: "Find invoices for one client email and an optional " +
			"invoice date range.",
		Params: obj(map[string]any{
			"client_email": str("Client email address"),
			"from_date":    str("First invoice date, YYYY-MM-DD, optional"),
			"to_date":      str("Last invoice date, YYYY-MM-DD, optional"),
		}, "client_email")},
	{Name: toolCashSummary,
		Description: "Totals of paid, unpaid, and overdue money for each " +
			"currency. Paid money counts only inside the period.",
		Params: obj(map[string]any{
			"period": map[string]any{"type": "string",
				"enum": []string{domain.PeriodThisMonth, domain.PeriodLastMonth,
					domain.PeriodLast30Days, domain.PeriodAll}},
		}, "period")},
	{Name: domain.ToolProposeInvoice,
		Description: "Propose a new invoice. The user sees a card and must " +
			"approve it. On approval PayPal creates and sends the invoice.",
		Params: obj(map[string]any{
			"client_name":  str("Client name"),
			"client_email": str("Client email address"),
			"currency":     str("ISO 4217 code: " + strings.Join(domain.AllowedCurrencies, ", ")),
			"items": map[string]any{"type": "array", "items": obj(map[string]any{
				"name":       str("What the client pays for"),
				"quantity":   map[string]any{"type": "number"},
				"unit_price": str(`Decimal with two places, for example "450.00"`),
			}, "name", "quantity", "unit_price")},
			"due_days": map[string]any{"type": "integer",
				"description": "Days from today until payment is due, 1 to 90"},
			"note": str("Optional note to the client"),
		}, "client_name", "client_email", "currency", "items", "due_days")},
	{Name: domain.ToolProposeReminder,
		Description: "Propose a payment reminder for an unpaid invoice. " +
			"The user must approve it.",
		Params: obj(map[string]any{
			"invoice_id": str("PayPal invoice ID"),
			"subject":    str("Short email subject"),
			"note":       str("Polite reminder text, two or three sentences"),
		}, "invoice_id", "subject", "note")},
	{Name: domain.ToolProposeCancel,
		Description: "Propose to cancel a sent invoice. The user must " +
			"approve it.",
		Params: obj(map[string]any{
			"invoice_id": str("PayPal invoice ID"),
			"reason":     str("Reason shown to the client"),
		}, "invoice_id", "reason")},
}

// runTool executes one function call and returns the result for the
// model. Errors become {"error": "..."} so the model can explain them.
func (s *Service) runTool(ctx context.Context, sessionID string,
	call contract.FuncCall, emit func(domain.Event)) map[string]any {
	out, err := s.dispatch(ctx, sessionID, call, emit)
	if err != nil {
		s.opt.Logger.Info("agent: tool error", "tool", call.Name, "err", err)
		return map[string]any{"error": userError(err)}
	}
	return out
}

func (s *Service) dispatch(ctx context.Context, sessionID string,
	call contract.FuncCall, emit func(domain.Event)) (map[string]any, error) {
	switch call.Name {
	case toolListInvoices:
		var a struct {
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		if a.Limit <= 0 || a.Limit > 50 {
			a.Limit = 10
		}
		invs, err := s.pp.List(ctx, strings.ToUpper(a.Status), a.Limit)
		if err != nil {
			return nil, err
		}
		s.cache(ctx, invs...)
		return map[string]any{"invoices": s.views(invs)}, nil

	case toolGetInvoice:
		var a struct {
			InvoiceID string `json:"invoice_id"`
		}
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		inv, err := s.pp.Get(ctx, a.InvoiceID)
		if err != nil {
			return nil, err
		}
		s.cache(ctx, inv)
		return map[string]any{"invoice": s.view(inv)}, nil

	case toolSearchInvoices:
		var a struct {
			ClientEmail string `json:"client_email"`
			FromDate    string `json:"from_date"`
			ToDate      string `json:"to_date"`
		}
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		from, err := parseDate(a.FromDate)
		if err != nil {
			return nil, err
		}
		to, err := parseDate(a.ToDate)
		if err != nil {
			return nil, err
		}
		invs, err := s.pp.Search(ctx, a.ClientEmail, from, to)
		if err != nil {
			return nil, err
		}
		s.cache(ctx, invs...)
		return map[string]any{"invoices": s.views(invs)}, nil

	case toolCashSummary:
		var a struct {
			Period string `json:"period"`
		}
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		// Refresh the cache first; on a PayPal error use what is cached.
		if invs, err := s.pp.List(ctx, "", 100); err == nil {
			s.cache(ctx, invs...)
		}
		invs, err := s.store.ListInvoices(ctx, "")
		if err != nil {
			return nil, err
		}
		sum, err := domain.Summarize(invs, a.Period, s.opt.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"period": a.Period, "totals": sum}, nil

	case domain.ToolProposeInvoice:
		var d domain.InvoiceDraft
		if err := decodeArgs(call.Args, &d); err != nil {
			return nil, err
		}
		d.Currency = strings.ToUpper(strings.TrimSpace(d.Currency))
		if err := domain.ValidateDraft(d); err != nil {
			return nil, err
		}
		total, _ := domain.DraftTotalCents(d)
		preview := map[string]any{
			"kind":     "invoice",
			"draft":    d,
			"total":    domain.FormatAmount(total),
			"due_date": d.DueDate(s.opt.Now()).Format(time.DateOnly),
		}
		return s.propose(ctx, sessionID, call.Name, "", d, preview, emit)

	case domain.ToolProposeReminder:
		var a struct {
			InvoiceID string `json:"invoice_id"`
			Subject   string `json:"subject"`
			Note      string `json:"note"`
		}
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		inv, err := s.pp.Get(ctx, a.InvoiceID)
		if err != nil {
			return nil, err
		}
		s.cache(ctx, inv)
		if b := domain.Bucket(inv, s.opt.Now()); b != domain.BucketUnpaid &&
			b != domain.BucketOverdue {
			return nil, fmt.Errorf("%w: invoice %s is %s; only unpaid "+
				"invoices get reminders", contract.ErrValidation, inv.Number,
				inv.Status)
		}
		if strings.TrimSpace(a.Note) == "" {
			return nil, fmt.Errorf("%w: the reminder note is empty",
				contract.ErrValidation)
		}
		if has, err := s.store.HasPendingAction(ctx, call.Name, inv.ID); err != nil {
			return nil, err
		} else if has {
			return nil, fmt.Errorf("%w: a reminder for invoice %s is already "+
				"waiting for approval", contract.ErrValidation, inv.Number)
		}
		preview := map[string]any{"kind": "reminder", "invoice": inv,
			"subject": a.Subject, "note": a.Note}
		return s.propose(ctx, sessionID, call.Name, inv.ID, a, preview, emit)

	case domain.ToolProposeCancel:
		var a struct {
			InvoiceID string `json:"invoice_id"`
			Reason    string `json:"reason"`
		}
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		inv, err := s.pp.Get(ctx, a.InvoiceID)
		if err != nil {
			return nil, err
		}
		s.cache(ctx, inv)
		if inv.Status != "SENT" && inv.Status != "UNPAID" {
			return nil, fmt.Errorf("%w: invoice %s is %s; only sent, unpaid "+
				"invoices can be cancelled", contract.ErrValidation,
				inv.Number, inv.Status)
		}
		preview := map[string]any{"kind": "cancel", "invoice": inv,
			"reason": a.Reason}
		return s.propose(ctx, sessionID, call.Name, inv.ID, a, preview, emit)
	}
	return nil, fmt.Errorf("%w: unknown tool %q", contract.ErrValidation, call.Name)
}

// propose saves a PendingAction and shows the approval card. Nothing is
// sent to PayPal here.
func (s *Service) propose(ctx context.Context, sessionID, tool, invoiceID string,
	args, preview any, emit func(domain.Event)) (map[string]any, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	previewJSON, err := json.Marshal(preview)
	if err != nil {
		return nil, err
	}
	a := domain.PendingAction{
		ID:        domain.NewID(),
		SessionID: sessionID,
		Tool:      tool,
		InvoiceID: invoiceID,
		Args:      argsJSON,
		Preview:   previewJSON,
		Status:    domain.ActionPending,
		RequestID: domain.NewID(),
		ExpiresAt: s.opt.Now().Add(domain.ChatActionTTL),
	}
	if err := s.store.SaveAction(ctx, a); err != nil {
		return nil, err
	}
	emitData(emit, domain.EventActionProposed, a)
	return map[string]any{
		"status":    "waiting_for_approval",
		"action_id": a.ID,
		"message":   "The user sees an approval card. Nothing is sent yet.",
	}, nil
}

func (s *Service) cache(ctx context.Context, invs ...domain.Invoice) {
	for _, inv := range invs {
		if err := s.store.UpsertInvoice(ctx, inv); err != nil {
			s.opt.Logger.Warn("agent: cache invoice", "id", inv.ID, "err", err)
		}
	}
}

// view is the compact invoice the model sees.
func (s *Service) view(inv domain.Invoice) map[string]any {
	v := map[string]any{
		"invoice_id":   inv.ID,
		"number":       inv.Number,
		"status":       inv.Status,
		"client_name":  inv.ClientName,
		"client_email": inv.ClientEmail,
		"total":        inv.Total + " " + inv.Currency,
		"bucket":       domain.Bucket(inv, s.opt.Now()),
	}
	if !inv.DueDate.IsZero() {
		v["due_date"] = inv.DueDate.Format(time.DateOnly)
	}
	return v
}

func (s *Service) views(invs []domain.Invoice) []map[string]any {
	out := make([]map[string]any, 0, len(invs))
	for _, inv := range invs {
		out = append(out, s.view(inv))
	}
	return out
}

func decodeArgs(args map[string]any, dst any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("%w: bad arguments: %v", contract.ErrValidation, err)
	}
	return nil
}

func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: date %q is not YYYY-MM-DD",
			contract.ErrValidation, s)
	}
	return t, nil
}

// userError turns an error into text that is safe to show the model and
// the user. Validation and not-found errors keep their message.
func userError(err error) string {
	switch {
	case errors.Is(err, contract.ErrValidation):
		return err.Error()
	case errors.Is(err, contract.ErrNotFound):
		return "Not found: " + err.Error()
	case errors.Is(err, contract.ErrRateLimited):
		return "PayPal is rate limiting requests. Try again soon."
	default:
		return "PayPal or the server had a problem. Try again."
	}
}

// toolSummary returns one short line about what a tool call did, for the
// user to see. It returns "" for a failed call, which the model explains,
// and for a proposal, whose card speaks for itself. out may come straight
// from a tool or back from JSON, so lists can be either slice type.
func toolSummary(name string, out map[string]any) string {
	if _, failed := out["error"]; failed {
		return ""
	}
	count := func(v any) int {
		switch l := v.(type) {
		case []map[string]any:
			return len(l)
		case []any:
			return len(l)
		}
		return 0
	}
	invoices := func(n int) string {
		if n == 1 {
			return "1 invoice"
		}
		return fmt.Sprintf("%d invoices", n)
	}
	switch name {
	case toolListInvoices:
		return "Read " + invoices(count(out["invoices"])) + " from PayPal"
	case toolSearchInvoices:
		return "Searched PayPal and found " + invoices(count(out["invoices"]))
	case toolGetInvoice:
		if inv, ok := out["invoice"].(map[string]any); ok {
			if n, _ := inv["number"].(string); n != "" {
				return "Read invoice " + n + " from PayPal"
			}
		}
		return "Read an invoice from PayPal"
	case toolCashSummary:
		return "Added up paid, unpaid, and overdue money from PayPal"
	}
	return ""
}

// photoSummary is the tool line for a photo that Gemini read.
const photoSummary = "Read the photo with Gemini"
