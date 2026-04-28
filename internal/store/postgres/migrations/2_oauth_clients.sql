-- Migration 2: OAuth2 client registrations (DCR)

CREATE TABLE IF NOT EXISTS oauth_clients (
    id                         UUID        PRIMARY KEY DEFAULT uuidv7(),
    client_id                  TEXT        NOT NULL UNIQUE,
    client_name                TEXT,
    redirect_uris              TEXT[]      NOT NULL,
    grant_types                TEXT[]      NOT NULL,
    response_types             TEXT[]      NOT NULL,
    token_endpoint_auth_method TEXT        NOT NULL DEFAULT 'none',
    scope                      TEXT,
    issued_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at                 TIMESTAMPTZ NOT NULL
);

-- Enables efficient cleanup of expired client registrations.
CREATE INDEX IF NOT EXISTS oauth_clients_expires_at ON oauth_clients (expires_at);

INSERT INTO schema_migrations (version) VALUES (2) ON CONFLICT DO NOTHING;
