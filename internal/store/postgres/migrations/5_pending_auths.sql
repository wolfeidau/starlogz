-- Migration 5: Pending authorization state (replaces in-memory map)

CREATE TABLE IF NOT EXISTS pending_auths (
    state          TEXT        NOT NULL PRIMARY KEY,
    client_id      TEXT,
    redirect_uri   TEXT        NOT NULL,
    scope          TEXT        NOT NULL,
    code_challenge TEXT        NOT NULL,
    client_state   TEXT,
    expires_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS pending_auths_expires_at ON pending_auths (expires_at);

INSERT INTO schema_migrations (version) VALUES (5) ON CONFLICT DO NOTHING;
