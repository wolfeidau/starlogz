-- Migration 13: Retain hashed refresh token history for grace retries and diagnostics

CREATE TABLE IF NOT EXISTS retired_refresh_tokens (
    token_hash      BYTEA       NOT NULL PRIMARY KEY,
    reason          TEXT        NOT NULL CHECK (reason IN (
        'rotated',
        'github_expired',
        'github_invalid',
        'github_missing_refresh',
        'grant_deleted'
    )),
    user_id         UUID        REFERENCES users(id) ON DELETE SET NULL,
    client_id       TEXT,
    old_jti         TEXT,
    replacement_jti TEXT,
    grace_expires_at TIMESTAMPTZ,
    retained_until  TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS retired_refresh_tokens_retained_until_idx
    ON retired_refresh_tokens (retained_until);

CREATE INDEX IF NOT EXISTS retired_refresh_tokens_replacement_jti_idx
    ON retired_refresh_tokens (replacement_jti)
    WHERE replacement_jti IS NOT NULL;

CREATE INDEX IF NOT EXISTS retired_refresh_tokens_client_reason_idx
    ON retired_refresh_tokens (client_id, reason);

INSERT INTO schema_migrations (version) VALUES (13) ON CONFLICT DO NOTHING;
