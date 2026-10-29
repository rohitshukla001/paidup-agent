package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validDraft() InvoiceDraft {
	return InvoiceDraft{
		ClientName:  "Jane",
		ClientEmail: "jane@example.com",
		Currency:    "USD",
		Items:       []LineItem{{Name: "Landing page", Quantity: 1, UnitPrice: "450.00"}},
		DueDays:     7,
	}
}

func TestValidateDraft(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*InvoiceDraft)
		wantErr string // substring; "" means valid
	}{
		{"valid", func(*InvoiceDraft) {}, ""},
		{"empty name", func(d *InvoiceDraft) { d.ClientName = " " }, "client_name"},
		{"bad email", func(d *InvoiceDraft) { d.ClientEmail = "jane" }, "client_email"},
		{"email with display name", func(d *InvoiceDraft) { d.ClientEmail = "Jane <jane@example.com>" }, "client_email"},
		{"currency not allowed", func(d *InvoiceDraft) { d.Currency = "INR" }, "currency"},
		{"due days zero", func(d *InvoiceDraft) { d.DueDays = 0 }, "due_days"},
		{"due days 91", func(d *InvoiceDraft) { d.DueDays = 91 }, "due_days"},
		{"due days 90", func(d *InvoiceDraft) { d.DueDays = 90 }, ""},
		{"no items", func(d *InvoiceDraft) { d.Items = nil }, "items is empty"},
		{"zero quantity", func(d *InvoiceDraft) { d.Items[0].Quantity = 0 }, "quantity"},
		{"bad price", func(d *InvoiceDraft) { d.Items[0].UnitPrice = "abc" }, "unit_price"},
		{"zero price", func(d *InvoiceDraft) { d.Items[0].UnitPrice = "0" }, "unit_price"},
		{"total at limit", func(d *InvoiceDraft) { d.Items[0].UnitPrice = "10000" }, ""},
		{"total over limit", func(d *InvoiceDraft) { d.Items[0].Quantity = 2; d.Items[0].UnitPrice = "5000.01" }, "total"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDraft()
			tt.change(&d)
			err := ValidateDraft(d)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("error does not wrap ErrValidation: %v", err)
			}
		})
	}
}

func TestParseAndFormatAmount(t *testing.T) {
	good := map[string]int64{"450": 45000, "450.5": 45050, "450.05": 45005, "0.99": 99, " 12.00 ": 1200}
	for in, want := range good {
		got, err := ParseAmount(in)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-1", "+1", "1.", ".5", "1.234", "1,000", "1.+5", "abc", "1e3"} {
		if _, err := ParseAmount(in); err == nil {
			t.Errorf("ParseAmount(%q) want error", in)
		}
	}
	if got := FormatAmount(45005); got != "450.05" {
		t.Errorf("FormatAmount = %q", got)
	}
}

func TestDraftTotalAndDueDate(t *testing.T) {
	d := validDraft()
	d.Items = append(d.Items, LineItem{Name: "Hours", Quantity: 1.5, UnitPrice: "33.33"})
	total, err := DraftTotalCents(d)
	if err != nil || total != 45000+5000 { // 1.5 * 33.33 = 49.995, rounds to 50.00
		t.Fatalf("total = %d, %v", total, err)
	}
	now := time.Date(2026, 11, 11, 23, 30, 0, 0, time.UTC)
	if got := d.DueDate(now); !got.Equal(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("DueDate = %v", got)
	}
}

func TestBucket(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		status string
		due    time.Time
		want   string
	}{
		{"PAID", today.AddDate(0, 0, -5), BucketPaid},
		{"MARKED_AS_PAID", time.Time{}, BucketPaid},
		{"SENT", today, BucketUnpaid},
		{"UNPAID", today.AddDate(0, 0, -1), BucketOverdue},
		{"PARTIALLY_PAID", today.AddDate(0, 0, 3), BucketUnpaid},
		{"PAYMENT_PENDING", time.Time{}, BucketUnpaid},
		{"DRAFT", today.AddDate(0, 0, -1), ""},
		{"CANCELLED", today, ""},
		{"REFUNDED", today, ""},
	}
	for _, tt := range tests {
		if got := Bucket(Invoice{Status: tt.status, DueDate: tt.due}, now); got != tt.want {
			t.Errorf("Bucket(%s, %v) = %q, want %q", tt.status, tt.due, got, tt.want)
		}
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	invs := []Invoice{
		{ID: "1", Status: "PAID", Currency: "USD", Total: "100.00", UpdatedAt: now.AddDate(0, 0, -2)},
		{ID: "2", Status: "PAID", Currency: "USD", Total: "50.00", UpdatedAt: now.AddDate(0, -1, 0)},
		{ID: "3", Status: "SENT", Currency: "USD", Total: "450.00", DueDate: now.AddDate(0, 0, 5)},
		{ID: "4", Status: "SENT", Currency: "USD", Total: "20.00", DueDate: now.AddDate(0, 0, -5)},
		{ID: "5", Status: "SENT", Currency: "EUR", Total: "10.00", DueDate: now.AddDate(0, 0, 1)},
		{ID: "6", Status: "DRAFT", Currency: "USD", Total: "999.00"},
	}
	got, err := Summarize(invs, PeriodThisMonth, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []Summary{
		{Currency: "EUR", Paid: "0.00", Unpaid: "10.00", Overdue: "0.00"},
		{Currency: "USD", Paid: "100.00", Unpaid: "450.00", Overdue: "20.00"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	all, _ := Summarize(invs, PeriodAll, now)
	if all[1].Paid != "150.00" {
		t.Errorf("all-time paid = %s, want 150.00", all[1].Paid)
	}
	if _, err := Summarize(invs, "yesterday", now); !errors.Is(err, ErrValidation) {
		t.Errorf("unknown period: want ErrValidation, got %v", err)
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b || len(a) != 36 || a[14] != '4' {
		t.Fatalf("bad IDs %q %q", a, b)
	}
}
