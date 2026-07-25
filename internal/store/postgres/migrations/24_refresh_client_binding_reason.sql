-- Migration 24: Distinguish refresh grants removed for missing client binding

ALTER TABLE retired_refresh_tokens
    DROP CONSTRAINT IF EXISTS retired_refresh_tokens_reason_check;

ALTER TABLE retired_refresh_tokens
    ADD CONSTRAINT retired_refresh_tokens_reason_check
    CHECK (reason IN (
        'rotated',
        'github_expired',
        'github_invalid',
        'github_missing_refresh',
        'grant_deleted',
        'client_binding_missing'
    ));

INSERT INTO schema_migrations (version) VALUES (24) ON CONFLICT DO NOTHING;
