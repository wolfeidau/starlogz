-- Migration 20: revision snapshots replace the insight content audit trigger.

DROP TRIGGER IF EXISTS audit_insights ON insights;

INSERT INTO schema_migrations (version) VALUES (20) ON CONFLICT DO NOTHING;
