-- Migration 27: Operator-control schema and credential-free grant auditing.

ALTER TABLE grants
    ADD COLUMN id UUID;

ALTER TABLE grants
    ALTER COLUMN id SET DEFAULT uuidv7();

ALTER TABLE grants
    ADD CONSTRAINT grants_id_not_null CHECK (id IS NOT NULL) NOT VALID;

CREATE TABLE operator_actions (
    id               UUID        PRIMARY KEY DEFAULT uuidv7(),
    actor_user_id    UUID        NOT NULL,
    action           TEXT        NOT NULL CHECK (action IN ('web_session.revoke', 'oauth_grant.revoke')),
    target_id        UUID        NOT NULL,
    target_user_id   UUID        NOT NULL,
    target_client_id TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX operator_actions_created_at_idx
    ON operator_actions (created_at DESC, id DESC);

ALTER TABLE retired_refresh_tokens
    DROP CONSTRAINT retired_refresh_tokens_reason_check;

ALTER TABLE retired_refresh_tokens
    ADD CONSTRAINT retired_refresh_tokens_reason_check
    CHECK (reason IN (
        'rotated',
        'github_expired',
        'github_invalid',
        'github_missing_refresh',
        'grant_deleted',
        'client_binding_missing',
        'operator_revoked'
    ));

-- Grant audit snapshots predate encrypted-token handling; retain lifecycle evidence without credentials.
CREATE OR REPLACE FUNCTION audit_grants_trigger_func()
RETURNS TRIGGER AS $$
DECLARE
    old_data JSONB;
    new_data JSONB;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        old_data := to_jsonb(OLD) - 'our_refresh_token' - 'access_token' - 'refresh_token';
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_data := to_jsonb(NEW) - 'our_refresh_token' - 'access_token' - 'refresh_token';
    END IF;

    INSERT INTO audit_log (table_name, operation, old_data, new_data)
    VALUES (TG_TABLE_NAME, TG_OP, old_data, new_data);
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_grants ON grants;

CREATE TRIGGER audit_grants
    AFTER INSERT OR UPDATE OR DELETE ON grants
    FOR EACH ROW EXECUTE FUNCTION audit_grants_trigger_func();

INSERT INTO schema_migrations (version) VALUES (27) ON CONFLICT DO NOTHING;
