-- Migration 25: Bind a sanitized CIMD client logo to pending authorization state

ALTER TABLE pending_auths
    ADD COLUMN IF NOT EXISTS client_logo_png BYTEA NOT NULL DEFAULT '\x',
    ADD CONSTRAINT pending_auths_client_logo_png_size
        CHECK (octet_length(client_logo_png) <= 98304);

INSERT INTO schema_migrations (version) VALUES (25) ON CONFLICT DO NOTHING;
