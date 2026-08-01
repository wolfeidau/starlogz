-- Migration 30: Ensure the staged not-null constraint exists for early version-27 deployments.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'grants'::regclass
          AND conname = 'grants_id_not_null'
    ) THEN
        ALTER TABLE grants
            ADD CONSTRAINT grants_id_not_null CHECK (id IS NOT NULL) NOT VALID;
    END IF;
END
$$;

INSERT INTO schema_migrations (version) VALUES (30) ON CONFLICT DO NOTHING;
