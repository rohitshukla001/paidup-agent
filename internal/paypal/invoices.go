package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

const (
	dateLayout  = "2006-01-02"
	maxPageSize = 100
)

// Wire types for the Invoicing v2 API. Only the fields PaidUp uses.

type money struct {
	CurrencyCode string `json:"currency_code"`
	Value        string `json:"value"`
}

type ppName struct {
	GivenName string `json:"given_name,omitempty"`
	Surname   string `json:"surname,omitempty"`
	FullName  string `json:"full_name,omitempty"`
}

type ppInvoice struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status,omitempty"`
	Detail struct {
		InvoiceNumber string `json:"invoice_number,omitempty"`
		InvoiceDate   string `json:"invoice_date,omitempty"`
		CurrencyCode  string `json:"currency_code"`
		Note          string `json:"note,omitempty"`
		PaymentTerm   struct {
			TermType string `json:"term_type,omitempty"`
			DueDate  string `json:"due_date,omitempty"`
		} `json:"payment_term"`
		Metadata *struct {
			LastUpdateTime   string `json:"last_update_time,omitempty"`
			RecipientViewURL string `json:"recipient_view_url,omitempty"`
		} `json:"metadata,omitempty"`
	} `json:"detail"`
	Invoicer *struct {
		EmailAddress string `json:"email_address,omitempty"`
	} `json:"invoicer,omitempty"`
	PrimaryRecipients []struct {
		BillingInfo struct {
			Name         ppName `json:"name"`
			EmailAddress string `json:"email_address"`
		} `json:"billing_info"`
	} `json:"primary_recipients"`
	Items []struct {
		Name          string `json:"name"`
		Quantity      string `json:"quantity"`
		UnitAmount    money  `json:"unit_amount"`
		UnitOfMeasure string `json:"unit_of_measure,omitempty"`
	} `json:"items,omitempty"`
	Amount *money `json:"amount,omitempty"`
}

// link is the reply PayPal sends when it does not return the full object.
type link struct {
	Href string `json:"href"`
	Rel  string `json:"rel"`
}

// buildInvoice maps a draft to the create-invoice request body.
func (c *Client) buildInvoice(d domain.InvoiceDraft, number string,
	now time.Time) ppInvoice {
	var p ppInvoice
	p.Detail.InvoiceNumber = number
	p.Detail.InvoiceDate = now.UTC().Format(dateLayout)
	p.Detail.CurrencyCode = d.Currency
	p.Detail.Note = d.Note
	p.Detail.PaymentTerm.TermType = "DUE_ON_DATE_SPECIFIED"
	p.Detail.PaymentTerm.DueDate = d.DueDate(now).Format(dateLayout)
	if c.cfg.InvoicerEmail != "" {
		p.Invoicer = &struct {
			EmailAddress string `json:"email_address,omitempty"`
		}{c.cfg.InvoicerEmail}
	}

	given, surname := splitName(d.ClientName)
	p.PrimaryRecipients = make([]struct {
		BillingInfo struct {
			Name         ppName `json:"name"`
			EmailAddress string `json:"email_address"`
		} `json:"billing_info"`
	}, 1)
	bi := &p.PrimaryRecipients[0].BillingInfo
	bi.Name = ppName{GivenName: given, Surname: surname}
	bi.EmailAddress = d.ClientEmail

	for _, it := range d.Items {
		p.Items = append(p.Items, struct {
			Name          string `json:"name"`
			Quantity      string `json:"quantity"`
			UnitAmount    money  `json:"unit_amount"`
			UnitOfMeasure string `json:"unit_of_measure,omitempty"`
		}{
			Name:          it.Name,
			Quantity:      strconv.FormatFloat(it.Quantity, 'f', -1, 64),
			UnitAmount:    money{CurrencyCode: d.Currency, Value: it.UnitPrice},
			UnitOfMeasure: "QUANTITY",
		})
	}
	return p
}

// splitName puts the last word in the surname and the rest in the given
// name. PayPal shows both, so a one-word name stays in given_name.
func splitName(full string) (given, surname string) {
	f := strings.Fields(full)
	if len(f) < 2 {
		return strings.TrimSpace(full), ""
	}
	return strings.Join(f[:len(f)-1], " "), f[len(f)-1]
}

// toDomain maps a PayPal invoice to the cached view.
func (c *Client) toDomain(p ppInvoice) domain.Invoice {
	inv := domain.Invoice{
		ID:       p.ID,
		Number:   p.Detail.InvoiceNumber,
		Status:   p.Status,
		Currency: p.Detail.CurrencyCode,
	}
	if len(p.PrimaryRecipients) > 0 {
		bi := p.PrimaryRecipients[0].BillingInfo
		inv.ClientEmail = bi.EmailAddress
		inv.ClientName = bi.Name.FullName
		if inv.ClientName == "" {
			inv.ClientName = strings.TrimSpace(
				bi.Name.GivenName + " " + bi.Name.Surname)
		}
	}
	if p.Amount != nil {
		inv.Total = p.Amount.Value
		if inv.Currency == "" {
			inv.Currency = p.Amount.CurrencyCode
		}
	}
	if t, err := time.Parse(dateLayout, p.Detail.PaymentTerm.DueDate); err == nil {
		inv.DueDate = t
	}
	inv.UpdatedAt = c.now().UTC()
	if m := p.Detail.Metadata; m != nil {
		inv.PayerViewURL = m.RecipientViewURL
		if t, err := time.Parse(time.RFC3339, m.LastUpdateTime); err == nil {
			inv.UpdatedAt = t.UTC()
		}
	}
	return inv
}

// CreateDraft gets the next invoice number and creates a DRAFT invoice.
// The draft must pass domain.ValidateDraft. The payer link exists only
// after Send.
func (c *Client) CreateDraft(ctx context.Context, d domain.InvoiceDraft,
	requestID string) (domain.Invoice, error) {
	if err := domain.ValidateDraft(d); err != nil {
		return domain.Invoice{}, err
	}
	var num struct {
		InvoiceNumber string `json:"invoice_number"`
	}
	if err := c.do(ctx, call{
		method: http.MethodPost,
		path:   "/v2/invoicing/generate-next-invoice-number",
		out:    &num,
	}); err != nil {
		return domain.Invoice{}, fmt.Errorf("next invoice number: %w", err)
	}

	// With "return=representation" PayPal returns the full invoice. Some
	// replies still carry only a link, so decode both shapes.
	var reply struct {
		ppInvoice
		link
	}
	if err := c.do(ctx, call{
		method:    http.MethodPost,
		path:      "/v2/invoicing/invoices",
		in:        c.buildInvoice(d, num.InvoiceNumber, c.now()),
		out:       &reply,
		requestID: requestID,
		prefer:    "return=representation",
	}); err != nil {
		return domain.Invoice{}, fmt.Errorf("create invoice: %w", err)
	}
	if reply.ID != "" {
		return c.toDomain(reply.ppInvoice), nil
	}
	if reply.Href == "" {
		return domain.Invoice{}, fmt.Errorf(
			"%w: create invoice: reply has no id or link", contract.ErrUpstream)
	}
	return c.Get(ctx, path.Base(reply.Href))
}

type notify struct {
	Subject         string `json:"subject,omitempty"`
	Note            string `json:"note,omitempty"`
	SendToInvoicer  bool   `json:"send_to_invoicer"`
	SendToRecipient bool   `json:"send_to_recipient"`
}

// Send emails the invoice to the client and moves it from DRAFT to SENT.
func (c *Client) Send(ctx context.Context, invoiceID, requestID string) error {
	return c.action(ctx, invoiceID, "send", requestID,
		notify{SendToRecipient: true})
}

// Remind emails a payment reminder to the client.
func (c *Client) Remind(ctx context.Context, invoiceID, subject, note,
	requestID string) error {
	return c.action(ctx, invoiceID, "remind", requestID,
		notify{Subject: subject, Note: note, SendToRecipient: true})
}

// Cancel cancels a sent invoice and tells the client why.
func (c *Client) Cancel(ctx context.Context, invoiceID, reason,
	requestID string) error {
	return c.action(ctx, invoiceID, "cancel", requestID,
		notify{Subject: "Invoice cancelled", Note: reason, SendToRecipient: true})
}

func (c *Client) action(ctx context.Context, invoiceID, verb, requestID string,
	body notify) error {
	if invoiceID == "" {
		return fmt.Errorf("%w: invoice ID is empty", contract.ErrValidation)
	}
	err := c.do(ctx, call{
		method:    http.MethodPost,
		path:      "/v2/invoicing/invoices/" + url.PathEscape(invoiceID) + "/" + verb,
		in:        body,
		requestID: requestID,
	})
	if err != nil {
		return fmt.Errorf("%s invoice %s: %w", verb, invoiceID, err)
	}
	return nil
}

// Get returns one invoice with its current status and payer link.
func (c *Client) Get(ctx context.Context, invoiceID string) (domain.Invoice, error) {
	if invoiceID == "" {
		return domain.Invoice{}, fmt.Errorf(
			"%w: invoice ID is empty", contract.ErrValidation)
	}
	var p ppInvoice
	if err := c.do(ctx, call{
		method: http.MethodGet,
		path:   "/v2/invoicing/invoices/" + url.PathEscape(invoiceID),
		out:    &p,
	}); err != nil {
		return domain.Invoice{}, fmt.Errorf("get invoice %s: %w", invoiceID, err)
	}
	return c.toDomain(p), nil
}

type page struct {
	Items []ppInvoice `json:"items"`
}

// List returns up to limit invoices with the PayPal status, or all
// statuses when status is "". The GET list endpoint has no status filter,
// so a status uses the search endpoint.
func (c *Client) List(ctx context.Context, status string,
	limit int) ([]domain.Invoice, error) {
	size := pageSize(limit)
	if status == "" {
		var pg page
		q := url.Values{"page": {"1"}, "page_size": {strconv.Itoa(size)},
			"total_required": {"false"}}
		if err := c.do(ctx, call{
			method: http.MethodGet,
			path:   "/v2/invoicing/invoices?" + q.Encode(),
			out:    &pg,
		}); err != nil {
			return nil, fmt.Errorf("list invoices: %w", err)
		}
		return c.mapPage(pg), nil
	}
	return c.search(ctx, map[string]any{"status": []string{status}}, size)
}

// Search returns invoices for one client email with an invoice date from
// "from" to "to", both dates included. An empty email or a zero time means
// no filter on that field.
func (c *Client) Search(ctx context.Context, email string,
	from, to time.Time) ([]domain.Invoice, error) {
	body := map[string]any{}
	if email != "" {
		body["recipient_email"] = email
	}
	if !from.IsZero() || !to.IsZero() {
		r := map[string]string{}
		if !from.IsZero() {
			r["start"] = from.UTC().Format(dateLayout)
		}
		if !to.IsZero() {
			r["end"] = to.UTC().Format(dateLayout)
		}
		body["invoice_date_range"] = r
	}
	return c.search(ctx, body, maxPageSize)
}

func (c *Client) search(ctx context.Context, body map[string]any,
	size int) ([]domain.Invoice, error) {
	var pg page
	q := url.Values{"page": {"1"}, "page_size": {strconv.Itoa(size)},
		"total_required": {"false"}}
	if err := c.do(ctx, call{
		method: http.MethodPost,
		path:   "/v2/invoicing/search-invoices?" + q.Encode(),
		in:     body,
		out:    &pg,
	}); err != nil {
		return nil, fmt.Errorf("search invoices: %w", err)
	}
	return c.mapPage(pg), nil
}

func (c *Client) mapPage(pg page) []domain.Invoice {
	out := make([]domain.Invoice, 0, len(pg.Items))
	for _, p := range pg.Items {
		out = append(out, c.toDomain(p))
	}
	return out
}

func pageSize(limit int) int {
	if limit <= 0 || limit > maxPageSize {
		return maxPageSize
	}
	return limit
}

// VerifyWebhook asks PayPal to check the signature of a webhook event.
// rawBody must be the request body exactly as received: a new JSON
// encoding of the event can change the bytes and fail the check. It
// returns false, with no error, when a signature header is missing.
func (c *Client) VerifyWebhook(ctx context.Context, h http.Header,
	rawBody []byte) (bool, error) {
	if c.cfg.WebhookID == "" {
		return false, fmt.Errorf("paypal: PAYPAL_WEBHOOK_ID is not set")
	}
	if !json.Valid(rawBody) {
		return false, nil
	}
	hdr := map[string]string{
		"auth_algo":         h.Get("PAYPAL-AUTH-ALGO"),
		"cert_url":          h.Get("PAYPAL-CERT-URL"),
		"transmission_id":   h.Get("PAYPAL-TRANSMISSION-ID"),
		"transmission_sig":  h.Get("PAYPAL-TRANSMISSION-SIG"),
		"transmission_time": h.Get("PAYPAL-TRANSMISSION-TIME"),
		"webhook_id":        c.cfg.WebhookID,
	}
	for _, v := range hdr {
		if v == "" {
			return false, nil
		}
	}
	// Build the body by hand. json.Marshal would compact the raw event
	// and change its bytes.
	head, err := json.Marshal(hdr)
	if err != nil {
		return false, err
	}
	payload := make([]byte, 0, len(head)+len(rawBody)+20)
	payload = append(payload, head[:len(head)-1]...)
	payload = append(payload, `,"webhook_event":`...)
	payload = append(payload, rawBody...)
	payload = append(payload, '}')

	var reply struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := c.do(ctx, call{
		method: http.MethodPost,
		path:   "/v1/notifications/verify-webhook-signature",
		rawIn:  payload,
		out:    &reply,
	}); err != nil {
		return false, fmt.Errorf("verify webhook: %w", err)
	}
	return reply.VerificationStatus == "SUCCESS", nil
}
