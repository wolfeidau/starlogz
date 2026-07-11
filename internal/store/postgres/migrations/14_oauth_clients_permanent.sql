-- Migration 14: Dynamic client registrations are permanent unless explicitly expired.

ALTER TABLE oauth_clients ALTER COLUMN expires_at DROP NOT NULL;

UPDATE oauth_clients SET expires_at = NULL;

INSERT INTO schema_migrations (version) VALUES (14) ON CONFLICT DO NOTHING;
