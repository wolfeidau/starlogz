-- Migration 15: Track client activity for future stale-registration cleanup.

ALTER TABLE oauth_clients ADD COLUMN last_used_at TIMESTAMPTZ;

UPDATE oauth_clients SET last_used_at = issued_at;

ALTER TABLE oauth_clients
    ALTER COLUMN last_used_at SET DEFAULT now(),
    ALTER COLUMN last_used_at SET NOT NULL;

CREATE INDEX oauth_clients_last_used_at_idx ON oauth_clients (last_used_at);

INSERT INTO schema_migrations (version) VALUES (15) ON CONFLICT DO NOTHING;
