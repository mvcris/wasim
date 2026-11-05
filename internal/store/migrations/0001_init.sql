CREATE TABLE contacts (
    wa_id           TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    last_inbound_at INTEGER,
    created_at      INTEGER NOT NULL
);

CREATE TABLE media (
    id         TEXT PRIMARY KEY,
    mime_type  TEXT NOT NULL,
    sha256     TEXT NOT NULL,
    file_size  INTEGER NOT NULL,
    filename   TEXT NOT NULL,
    path       TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE messages (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT NOT NULL UNIQUE,
    contact_wa_id TEXT NOT NULL REFERENCES contacts (wa_id) ON DELETE CASCADE,
    direction     TEXT NOT NULL,
    type          TEXT NOT NULL,
    status        TEXT NOT NULL,
    rendered      TEXT NOT NULL,
    raw           TEXT NOT NULL,
    context_id    TEXT NOT NULL,
    media_id      TEXT NOT NULL,
    category      TEXT NOT NULL,
    error_code    INTEGER NOT NULL,
    error_title   TEXT NOT NULL,
    timestamp     INTEGER NOT NULL
);

CREATE INDEX idx_messages_contact ON messages (contact_wa_id, seq);

CREATE TABLE templates (
    seq              INTEGER PRIMARY KEY AUTOINCREMENT,
    id               TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL,
    language         TEXT NOT NULL,
    status           TEXT NOT NULL,
    category         TEXT NOT NULL,
    parameter_format TEXT NOT NULL,
    components       TEXT NOT NULL,
    UNIQUE (name, language)
);

CREATE TABLE failure_rules (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    contact_wa_id TEXT NOT NULL,
    code          INTEGER NOT NULL,
    channel       TEXT NOT NULL,
    remaining     INTEGER NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE webhook_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    kind            TEXT NOT NULL,
    state           TEXT NOT NULL,
    body            BLOB NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_status     INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    message_id      TEXT NOT NULL,
    message_status  TEXT NOT NULL,
    error_code      INTEGER NOT NULL DEFAULT 0,
    error_title     TEXT NOT NULL DEFAULT '',
    bad_signature   INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    finished_at     INTEGER
);

CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (state, next_attempt_at);
