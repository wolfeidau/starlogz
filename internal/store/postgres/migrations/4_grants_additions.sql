-- Migration 4: Add refresh token and client_id columns to grants

ALTER TABLE grants
    ADD COLUMN IF NOT EXISTS our_refresh_token TEXT,
    ADD COLUMN IF NOT EXISTS client_id         TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS grants_our_refresh_token_idx
    ON grants (our_refresh_token)
    WHERE our_refresh_token IS NOT NULL;

INSERT INTO schema_migrations (version) VALUES (4) ON CONFLICT DO NOTHING;
