-- Migration 9: Add scope column to grants so the refresh-token grant can return
-- the same scope the client originally requested.

ALTER TABLE grants
    ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT '';

INSERT INTO schema_migrations (version) VALUES (9) ON CONFLICT DO NOTHING;
