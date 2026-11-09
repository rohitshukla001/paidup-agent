package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq" // registers the "postgres" driver

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
	"paidup-agent/migrations"
)

// Store implements contract.Store.
type Store struct {
	db *sql.DB
}

var _ contract.Store = (*Store)(nil)

// New connects, checks the connection, and runs pending migrations.
func New(ctx context.Context, dsn string) (contract.Store, error) {
	return Open(ctx, dsn)
}

// Open is New with the concrete type, for tests and tools.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("postgres: DATABASE_URL is not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// migrate runs each file in the migrations package that has not run yet,
// in name order, each in its own transaction. An advisory lock stops two
// server instances from migrating at the same time.
func (s *Store) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS
		schema_migrations (version text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("postgres: create schema_migrations: %w", err)
	}
	for _, name := range names {
		if err := s.migrateOne(ctx, name); err != nil {
			return fmt.Errorf("postgres: migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) migrateOne(ctx context.Context, name string) error {
	body, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 0x70616964 is "paid" in ASCII: any constant works.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1885432164)`); err != nil {
		return err
	}
	var done bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM
		schema_migrations WHERE version = $1)`, name).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations
		(version) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateSession(ctx context.Context) (string, error) {
	id := domain.NewID()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id) VALUES ($1)`, id)
	return id, wrap("create session", err)
}

func (s *Store) CountAIRequests(ctx context.Context, sessionID string,
	since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_requests
		WHERE session_id = $1 AND created_at >= $2`, sessionID, since).Scan(&n)
	return n, wrap("count AI requests", err)
}

func (s *Store) RecordAIRequest(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_requests
		(session_id, created_at) VALUES ($1, now())`, sessionID)
	return wrap("record AI request", err)
}

func (s *Store) AddMessage(ctx context.Context, m domain.Message) error {
	if m.ID == "" {
		m.ID = domain.NewID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO messages
		(id, session_id, role, content, image_ref, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		m.ID, m.SessionID, m.Role, m.Content, m.ImageRef, m.CreatedAt)
	return wrap("add message", err)
}

func (s *Store) RecentMessages(ctx context.Context, sessionID string,
	limit int) ([]domain.Message, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, role,
		content, image_ref, created_at FROM (
			SELECT * FROM messages WHERE session_id = $1
			ORDER BY seq DESC LIMIT $2
		) recent ORDER BY seq ASC`, sessionID, limit)
	if err != nil {
		return nil, wrap("recent messages", err)
	}
	defer rows.Close()
	out := []domain.Message{}
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content,
			&m.ImageRef, &m.CreatedAt); err != nil {
			return nil, wrap("recent messages", err)
		}
		m.CreatedAt = m.CreatedAt.UTC()
		out = append(out, m)
	}
	return out, wrap("recent messages", rows.Err())
}

const actionColumns = `id, session_id, tool, invoice_id, args, preview,
	status, request_id, result, expires_at`

func scanAction(row interface{ Scan(...any) error }) (domain.PendingAction, error) {
	var a domain.PendingAction
	var args, preview, result []byte
	var status string
	err := row.Scan(&a.ID, &a.SessionID, &a.Tool, &a.InvoiceID, &args,
		&preview, &status, &a.RequestID, &result, &a.ExpiresAt)
	if err != nil {
		return a, err
	}
	a.Args, a.Preview, a.Status = args, preview, domain.ActionStatus(status)
	if result != nil {
		a.Result = result
	}
	a.ExpiresAt = a.ExpiresAt.UTC()
	return a, nil
}

func (s *Store) SaveAction(ctx context.Context, a domain.PendingAction) error {
	if a.ID == "" {
		return fmt.Errorf("%w: action has no ID", contract.ErrValidation)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pending_actions
		(id, session_id, tool, invoice_id, args, preview, status,
		 request_id, result, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		a.ID, a.SessionID, a.Tool, a.InvoiceID, jsonOr(a.Args, "{}"),
		jsonOr(a.Preview, "{}"), string(a.Status), a.RequestID,
		nullJSON(a.Result), a.ExpiresAt)
	return wrap("save action", err)
}

func (s *Store) GetAction(ctx context.Context, id string) (domain.PendingAction, error) {
	a, err := scanAction(s.db.QueryRowContext(ctx,
		`SELECT `+actionColumns+` FROM pending_actions WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: action %s", contract.ErrNotFound, id)
	}
	return a, wrap("get action", err)
}

func (s *Store) ListActions(ctx context.Context,
	status domain.ActionStatus) ([]domain.PendingAction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+actionColumns+`
		FROM pending_actions WHERE $1 = '' OR status = $1
		ORDER BY expires_at ASC`, string(status))
	if err != nil {
		return nil, wrap("list actions", err)
	}
	defer rows.Close()
	out := []domain.PendingAction{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, wrap("list actions", err)
		}
		out = append(out, a)
	}
	return out, wrap("list actions", rows.Err())
}

// TransitionAction is one UPDATE with the old status in the WHERE clause,
// so only one of two concurrent callers can win.
func (s *Store) TransitionAction(ctx context.Context, id string,
	from, to domain.ActionStatus) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE pending_actions SET status = $3
		WHERE id = $1 AND status = $2`, id, string(from), string(to))
	if err != nil {
		return false, wrap("transition action", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	if _, err := s.GetAction(ctx, id); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) SetActionResult(ctx context.Context, id string,
	to domain.ActionStatus, result json.RawMessage) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pending_actions
		SET status = $2, result = $3 WHERE id = $1`,
		id, string(to), nullJSON(result))
	if err != nil {
		return wrap("set action result", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: action %s", contract.ErrNotFound, id)
	}
	return nil
}

// HasPendingAction ignores expired actions, so an old card that nobody
// approved does not block a new one forever.
func (s *Store) HasPendingAction(ctx context.Context, tool,
	invoiceID string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM
		pending_actions WHERE status = 'PENDING' AND tool = $1
		AND invoice_id = $2 AND expires_at > now())`, tool, invoiceID).Scan(&ok)
	return ok, wrap("has pending action", err)
}

// rankSQL returns a SQL expression that gives the rank of the status in
// col, from the same table as domain.StatusRank.
func rankSQL(col string) string {
	ranks := domain.StatusRanks()
	names := make([]string, 0, len(ranks))
	for n := range ranks {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("CASE " + col)
	for _, n := range names {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", n, ranks[n])
	}
	b.WriteString(" ELSE 1 END")
	return b.String()
}

// upsertInvoiceSQL keeps the newer copy by domain.IsNewerCopy: a status
// further along wins, and for the same rank the later updated_at wins.
var upsertInvoiceSQL = `INSERT INTO invoice_cache
		(id, number, status, client_name, client_email, currency, total,
		 due_date, payer_view_url, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET
			number = EXCLUDED.number, status = EXCLUDED.status,
			client_name = EXCLUDED.client_name,
			client_email = EXCLUDED.client_email,
			currency = EXCLUDED.currency, total = EXCLUDED.total,
			due_date = EXCLUDED.due_date,
			payer_view_url = CASE WHEN EXCLUDED.payer_view_url <> ''
				THEN EXCLUDED.payer_view_url
				ELSE invoice_cache.payer_view_url END,
			updated_at = EXCLUDED.updated_at
		WHERE ` + rankSQL("EXCLUDED.status") + ` > ` +
	rankSQL("invoice_cache.status") + `
		OR (` + rankSQL("EXCLUDED.status") + ` = ` +
	rankSQL("invoice_cache.status") + `
			AND invoice_cache.updated_at <= EXCLUDED.updated_at)`

// UpsertInvoice keeps the newest copy. See upsertInvoiceSQL.
func (s *Store) UpsertInvoice(ctx context.Context, inv domain.Invoice) error {
	if inv.UpdatedAt.IsZero() {
		inv.UpdatedAt = time.Now()
	}
	var due sql.NullTime
	if !inv.DueDate.IsZero() {
		due = sql.NullTime{Time: inv.DueDate, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, upsertInvoiceSQL,
		inv.ID, inv.Number, inv.Status, inv.ClientName, inv.ClientEmail,
		inv.Currency, inv.Total, due, inv.PayerViewURL, inv.UpdatedAt)
	return wrap("upsert invoice", err)
}

// ListInvoices returns the newest invoice number first. Sorting by length
// first puts "10" after "9" when PayPal does not pad the numbers.
func (s *Store) ListInvoices(ctx context.Context, status string) ([]domain.Invoice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, number, status,
		client_name, client_email, currency, total, due_date,
		payer_view_url, updated_at FROM invoice_cache
		WHERE $1 = '' OR status = $1
		ORDER BY length(number) DESC, number DESC, id DESC`, status)
	if err != nil {
		return nil, wrap("list invoices", err)
	}
	defer rows.Close()
	out := []domain.Invoice{}
	for rows.Next() {
		var inv domain.Invoice
		var due sql.NullTime
		if err := rows.Scan(&inv.ID, &inv.Number, &inv.Status,
			&inv.ClientName, &inv.ClientEmail, &inv.Currency, &inv.Total,
			&due, &inv.PayerViewURL, &inv.UpdatedAt); err != nil {
			return nil, wrap("list invoices", err)
		}
		if due.Valid {
			t := due.Time
			inv.DueDate = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0,
				time.UTC)
		}
		inv.UpdatedAt = inv.UpdatedAt.UTC()
		out = append(out, inv)
	}
	return out, wrap("list invoices", rows.Err())
}

func (s *Store) SaveEventOnce(ctx context.Context, eventID, eventType string,
	payload []byte) (bool, error) {
	if payload == nil {
		payload = []byte{}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO webhook_events
		(event_id, type, payload) VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, eventID, eventType, payload)
	if err != nil {
		return false, wrap("save event", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) AddReminder(ctx context.Context, invoiceID, note string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO reminders
		(invoice_id, note, sent_at) VALUES ($1, $2, now())`, invoiceID, note)
	return wrap("add reminder", err)
}

func (s *Store) LastReminder(ctx context.Context,
	invoiceID string) (time.Time, bool, error) {
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT max(sent_at) FROM reminders
		WHERE invoice_id = $1`, invoiceID).Scan(&t)
	if err != nil {
		return time.Time{}, false, wrap("last reminder", err)
	}
	return t.Time.UTC(), t.Valid, nil
}

// Reset deletes all demo data. The schema and its version stay.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `TRUNCATE sessions, ai_requests,
		messages, pending_actions, invoice_cache, webhook_events, reminders`)
	return wrap("reset", err)
}

func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("postgres: %s: %w", what, err)
}

func jsonOr(b json.RawMessage, def string) string {
	if len(strings.TrimSpace(string(b))) == 0 {
		return def
	}
	return string(b)
}

func nullJSON(b json.RawMessage) any {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return string(b)
}
