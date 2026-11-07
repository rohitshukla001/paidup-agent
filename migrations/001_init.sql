-- PaidUp schema, version 1. See Section 4.7 of docs/implementation-plan.md.

CREATE TABLE sessions (
    id         text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_requests (
    session_id text        NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX ai_requests_session_time ON ai_requests (session_id, created_at);

-- seq keeps insert order, which created_at alone cannot promise.
CREATE TABLE messages (
    seq        bigserial   PRIMARY KEY,
    id         text        NOT NULL UNIQUE,
    session_id text        NOT NULL,
    role       text        NOT NULL,
    content    text        NOT NULL,
    image_ref  text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL
);
CREATE INDEX messages_session_seq ON messages (session_id, seq);

CREATE TABLE pending_actions (
    id         text        PRIMARY KEY,
    session_id text        NOT NULL DEFAULT '',
    tool       text        NOT NULL,
    invoice_id text        NOT NULL DEFAULT '',
    args       jsonb       NOT NULL,
    preview    jsonb       NOT NULL,
    status     text        NOT NULL,
    request_id text        NOT NULL,
    result     jsonb,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pending_actions_status ON pending_actions (status, expires_at);
CREATE INDEX pending_actions_tool_invoice ON pending_actions (tool, invoice_id)
    WHERE status = 'PENDING';

CREATE TABLE invoice_cache (
    id             text        PRIMARY KEY,
    number         text        NOT NULL DEFAULT '',
    status         text        NOT NULL DEFAULT '',
    client_name    text        NOT NULL DEFAULT '',
    client_email   text        NOT NULL DEFAULT '',
    currency       text        NOT NULL DEFAULT '',
    total          text        NOT NULL DEFAULT '',
    due_date       date,
    payer_view_url text        NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL
);

-- payload is bytea: the raw webhook body, byte for byte.
CREATE TABLE webhook_events (
    event_id    text        PRIMARY KEY,
    type        text        NOT NULL,
    payload     bytea       NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

-- A row records a reminder that PayPal sent.
CREATE TABLE reminders (
    id         bigserial   PRIMARY KEY,
    invoice_id text        NOT NULL,
    note       text        NOT NULL,
    sent_at    timestamptz NOT NULL
);
CREATE INDEX reminders_invoice_time ON reminders (invoice_id, sent_at);
