-- Migration 6: Authorization codes (replaces in-memory map)

CREATE TABLE IF NOT EXISTS auth_codes (
    code                 TEXT        NOT NULL PRIMARY KEY,
    sub                  TEXT        NOT NULL,
    github_id            BIGINT      NOT NULL,
    email                TEXT        NOT NULL,
    scope                TEXT        NOT NULL,
    code_challenge       TEXT        NOT NULL,
    redirect_uri         TEXT        NOT NULL,
    client_id            TEXT,
    access_token         BYTEA       NOT NULL DEFAULT '\x',
    refresh_token        BYTEA       NOT NULL DEFAULT '\x',
    access_token_expiry  TIMESTAMPTZ,
    refresh_token_expiry TIMESTAMPTZ,
    expires_at           TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS auth_codes_expires_at ON auth_codes (expires_at);

INSERT INTO schema_migrations (version) VALUES (6) ON CONFLICT DO NOTHING;
