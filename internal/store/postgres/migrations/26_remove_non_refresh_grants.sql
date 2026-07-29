-- Migration 26: Grants are refresh-capable credentials only.

DELETE FROM grants WHERE our_refresh_token IS NULL OR our_refresh_token = '';

ALTER TABLE grants ALTER COLUMN our_refresh_token SET NOT NULL;

ALTER TABLE grants
    ADD CONSTRAINT grants_our_refresh_token_nonempty CHECK (our_refresh_token <> '');

INSERT INTO schema_migrations (version) VALUES (26) ON CONFLICT DO NOTHING;
