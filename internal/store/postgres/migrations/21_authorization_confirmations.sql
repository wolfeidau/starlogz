-- Migration 21: Post-GitHub authorization confirmation

ALTER TABLE pending_auths
    ADD COLUMN IF NOT EXISTS client_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS confirmation_required BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS authorization_confirmations (
    token_hash           BYTEA       NOT NULL PRIMARY KEY,
    sub                  TEXT        NOT NULL,
    github_id            BIGINT      NOT NULL,
    email                TEXT        NOT NULL,
    scope                TEXT        NOT NULL,
    code_challenge       TEXT        NOT NULL,
    redirect_uri         TEXT        NOT NULL,
    client_id            TEXT,
    client_name          TEXT        NOT NULL DEFAULT '',
    client_state         TEXT,
    access_token         BYTEA       NOT NULL DEFAULT '\x',
    refresh_token        BYTEA       NOT NULL DEFAULT '\x',
    access_token_expiry  TIMESTAMPTZ,
    refresh_token_expiry TIMESTAMPTZ,
    expires_at           TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS authorization_confirmations_expires_at
    ON authorization_confirmations (expires_at);

INSERT INTO schema_migrations (version) VALUES (21) ON CONFLICT DO NOTHING;
