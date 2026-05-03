-- Migration 7: Revoked JWT IDs (replaces in-memory blocklist)

CREATE TABLE IF NOT EXISTS revoked_tokens (
    jti        TEXT        NOT NULL PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS revoked_tokens_expires_at ON revoked_tokens (expires_at);

INSERT INTO schema_migrations (version) VALUES (7) ON CONFLICT DO NOTHING;
