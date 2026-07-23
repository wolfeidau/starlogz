-- Migration 22: Bind authorization-time refresh eligibility for CIMD and DCR clients

ALTER TABLE pending_auths
    ADD COLUMN IF NOT EXISTS refresh_allowed BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE auth_codes
    ADD COLUMN IF NOT EXISTS refresh_allowed BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE authorization_confirmations
    ADD COLUMN IF NOT EXISTS refresh_allowed BOOLEAN NOT NULL DEFAULT FALSE;

INSERT INTO schema_migrations (version) VALUES (22) ON CONFLICT DO NOTHING;
