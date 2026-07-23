-- Migration 23: Bind the resolved OAuth client kind for confirmation

ALTER TABLE pending_auths
    ADD COLUMN IF NOT EXISTS client_kind TEXT NOT NULL DEFAULT 'registered'
    CHECK (client_kind IN ('registered', 'cimd'));

INSERT INTO schema_migrations (version) VALUES (23) ON CONFLICT DO NOTHING;
