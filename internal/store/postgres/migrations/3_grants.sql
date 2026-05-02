-- Migration 3: Authorization grants table for persisting GitHub App tokens per session

CREATE TABLE IF NOT EXISTS grants (
    jti                   TEXT        NOT NULL PRIMARY KEY,
    github_id             BIGINT      NOT NULL REFERENCES users(github_id) ON DELETE CASCADE,
    access_token          BYTEA       NOT NULL,
    refresh_token         BYTEA       NOT NULL,
    access_token_expiry   TIMESTAMPTZ NOT NULL,
    refresh_token_expiry  TIMESTAMPTZ NOT NULL,
    jwt_expiry            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS grants_github_id_idx ON grants (github_id);

INSERT INTO schema_migrations (version) VALUES (3) ON CONFLICT DO NOTHING;
