package domain

import (
	"errors"
	"fmt"
	"math"
	"net/mail"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AllowedCurrencies lists the invoice currencies the agent accepts.
var AllowedCurrencies = []string{"USD", "EUR", "GBP", "CAD", "AUD"}

// Limits applied by ValidateDraft.
const (
	MaxInvoiceTotalCents = 10_000_00
	MinDueDays           = 1
	MaxDueDays           = 90
)

// Buckets returned by Bucket.
const (
	BucketPaid    = "paid"
	BucketUnpaid  = "unpaid"
	BucketOverdue = "overdue"
)

// Periods accepted by Summarize.
const (
	PeriodThisMonth  = "this_month"
	PeriodLastMonth  = "last_month"
	PeriodLast30Days = "last_30_days"
	PeriodAll        = "all"
)

// ErrValidation wraps every error that ValidateDraft returns.
var ErrValidation = errors.New("validation")

// ValidationError lists every problem found in a draft.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid invoice draft: " + strings.Join(e.Problems, "; ")
}

// Unwrap lets callers test with errors.Is(err, ErrValidation).
func (e *ValidationError) Unwrap() error { return ErrValidation }

// ValidateDraft checks a draft against the rules in Section 4.6 of the plan.
// It returns nil or a *ValidationError.
func ValidateDraft(d InvoiceDraft) error {
	var p []string

	if strings.TrimSpace(d.ClientName) == "" {
		p = append(p, "client_name is empty")
	}
	if !validEmail(d.ClientEmail) {
		p = append(p, fmt.Sprintf("client_email %q is not a valid address", d.ClientEmail))
	}
	if !slices.Contains(AllowedCurrencies, d.Currency) {
		p = append(p, fmt.Sprintf("currency %q is not one of %v", d.Currency, AllowedCurrencies))
	}
	if d.DueDays < MinDueDays || d.DueDays > MaxDueDays {
		p = append(p, fmt.Sprintf("due_days %d is not from %d to %d", d.DueDays, MinDueDays, MaxDueDays))
	}
	if len(d.Items) == 0 {
		p = append(p, "items is empty")
	}
	for i, it := range d.Items {
		if strings.TrimSpace(it.Name) == "" {
			p = append(p, fmt.Sprintf("items[%d].name is empty", i))
		}
		if !(it.Quantity > 0) || math.IsInf(it.Quantity, 0) {
			p = append(p, fmt.Sprintf("items[%d].quantity must be more than 0", i))
		}
		c, err := ParseAmount(it.UnitPrice)
		if err != nil || c <= 0 {
			p = append(p, fmt.Sprintf("items[%d].unit_price %q must be a positive amount", i, it.UnitPrice))
		}
	}
	if len(p) == 0 {
		total, err := DraftTotalCents(d)
		switch {
		case err != nil:
			p = append(p, err.Error())
		case total <= 0 || total > MaxInvoiceTotalCents:
			p = append(p, fmt.Sprintf("total %s must be more than 0 and not more than %s", FormatAmount(total), FormatAmount(MaxInvoiceTotalCents)))
		}
	}
	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}

func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}

// DraftTotalCents returns the sum of quantity times unit price, rounded
// to cents.
func DraftTotalCents(d InvoiceDraft) (int64, error) {
	var total int64
	for i, it := range d.Items {
		c, err := ParseAmount(it.UnitPrice)
		if err != nil {
			return 0, fmt.Errorf("items[%d].unit_price: %w", i, err)
		}
		line := math.Round(float64(c) * it.Quantity)
		if line > math.MaxInt64/4 {
			return 0, fmt.Errorf("items[%d] total is too large", i)
		}
		total += int64(line)
	}
	return total, nil
}

// DueDate returns the calendar date that is d.DueDays after now, in UTC.
func (d InvoiceDraft) DueDate(now time.Time) time.Time {
	return startOfDay(now).AddDate(0, 0, d.DueDays)
}

// ParseAmount converts a decimal string such as "450" or "450.5" to cents.
// It refuses more than two decimal places and negative values.
func ParseAmount(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("amount is empty")
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if !digitsOnly(whole) || (hasDot && !digitsOnly(frac) && frac != "") {
		return 0, fmt.Errorf("amount %q is not valid", s)
	}
	if hasDot && (len(frac) == 0 || len(frac) > 2) {
		return 0, fmt.Errorf("amount %q must have one or two decimal places", s)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > math.MaxInt64/100-1 {
		return 0, fmt.Errorf("amount %q is not valid", s)
	}
	var f int64
	if hasDot {
		if len(frac) == 1 {
			frac += "0"
		}
		f, err = strconv.ParseInt(frac, 10, 64)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("amount %q is not valid", s)
		}
	}
	return w*100 + f, nil
}

// FormatAmount converts cents to a decimal string with two places.
func FormatAmount(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// Bucket puts an invoice in "paid", "unpaid", "overdue", or "" (not counted).
// An invoice is overdue when its due date is before today (UTC).
func Bucket(inv Invoice, now time.Time) string {
	switch inv.Status {
	case "PAID", "MARKED_AS_PAID":
		return BucketPaid
	case "SENT", "UNPAID", "PARTIALLY_PAID", "PAYMENT_PENDING":
		if !inv.DueDate.IsZero() && inv.DueDate.Before(startOfDay(now)) {
			return BucketOverdue
		}
		return BucketUnpaid
	default:
		return ""
	}
}

// ValidPeriod reports whether p is a period that Summarize accepts.
func ValidPeriod(p string) bool {
	switch p {
	case PeriodThisMonth, PeriodLastMonth, PeriodLast30Days, PeriodAll:
		return true
	}
	return false
}

// Summarize returns one Summary for each currency, sorted by currency.
//
// The period applies to paid invoices only, by UpdatedAt (the time the cache
// saw the payment). Unpaid and overdue totals always show the current state,
// because money that is owed now is owed whatever the period.
func Summarize(invs []Invoice, period string, now time.Time) ([]Summary, error) {
	if !ValidPeriod(period) {
		return nil, fmt.Errorf("%w: unknown period %q", ErrValidation, period)
	}
	from, to := periodRange(period, now)

	type sums struct{ paid, unpaid, overdue int64 }
	by := map[string]*sums{}
	for _, inv := range invs {
		b := Bucket(inv, now)
		if b == "" {
			continue
		}
		c, err := ParseAmount(inv.Total)
		if err != nil {
			return nil, fmt.Errorf("invoice %s: %w", inv.ID, err)
		}
		s := by[inv.Currency]
		if s == nil {
			s = &sums{}
			by[inv.Currency] = s
		}
		switch b {
		case BucketPaid:
			if inRange(inv.UpdatedAt, from, to) {
				s.paid += c
			}
		case BucketUnpaid:
			s.unpaid += c
		case BucketOverdue:
			s.overdue += c
		}
	}

	out := make([]Summary, 0, len(by))
	for cur, s := range by {
		out = append(out, Summary{
			Currency: cur,
			Paid:     FormatAmount(s.paid),
			Unpaid:   FormatAmount(s.unpaid),
			Overdue:  FormatAmount(s.overdue),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out, nil
}

func periodRange(period string, now time.Time) (from, to time.Time) {
	now = now.UTC()
	firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	switch period {
	case PeriodThisMonth:
		return firstOfMonth, firstOfMonth.AddDate(0, 1, 0)
	case PeriodLastMonth:
		return firstOfMonth.AddDate(0, -1, 0), firstOfMonth
	case PeriodLast30Days:
		return now.AddDate(0, 0, -30), now.Add(time.Nanosecond)
	default:
		return time.Time{}, time.Time{}
	}
}

func inRange(t, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	return !t.Before(from) && t.Before(to)
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func startOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
