-- Migration 28: Backfill stable grant IDs and remove credentials from historical audit rows.

UPDATE grants
SET id = uuidv7()
WHERE id IS NULL;

UPDATE audit_log
SET old_data = CASE
        WHEN old_data IS NULL THEN NULL
        ELSE old_data - 'our_refresh_token' - 'access_token' - 'refresh_token'
    END,
    new_data = CASE
        WHEN new_data IS NULL THEN NULL
        ELSE new_data - 'our_refresh_token' - 'access_token' - 'refresh_token'
    END
WHERE table_name = 'grants'
  AND (
      old_data ?| ARRAY['our_refresh_token', 'access_token', 'refresh_token']
      OR new_data ?| ARRAY['our_refresh_token', 'access_token', 'refresh_token']
  );

INSERT INTO schema_migrations (version) VALUES (28) ON CONFLICT DO NOTHING;
