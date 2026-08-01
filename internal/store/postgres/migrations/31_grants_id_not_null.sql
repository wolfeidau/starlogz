-- Migration 31: Finalize the stable grant ID after backfill and concurrent indexing.

ALTER TABLE grants
    VALIDATE CONSTRAINT grants_id_not_null;

ALTER TABLE grants
    ALTER COLUMN id SET NOT NULL;

ALTER TABLE grants
    DROP CONSTRAINT grants_id_not_null;

INSERT INTO schema_migrations (version) VALUES (31) ON CONFLICT DO NOTHING;
