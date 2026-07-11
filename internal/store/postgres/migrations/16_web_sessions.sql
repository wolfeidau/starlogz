-- Migration 16: Durable GitHub profiles and opaque browser sessions.

ALTER TABLE users
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN avatar_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN profile_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN bio TEXT NOT NULL DEFAULT '',
    ADD COLUMN profile_updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE TABLE web_sessions (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    token_hash      BYTEA NOT NULL UNIQUE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    idle_expires_at TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,
    CHECK (idle_expires_at <= expires_at)
);

CREATE INDEX web_sessions_user_id_idx ON web_sessions (user_id);
CREATE INDEX web_sessions_expiry_idx ON web_sessions (expires_at) WHERE revoked_at IS NULL;

-- Session activity updates are high-volume and token hashes are credentials. Audit
-- login/revocation lifecycle changes while excluding both touch churn and token hashes.
CREATE OR REPLACE FUNCTION audit_web_sessions_trigger_func()
RETURNS TRIGGER AS $$
DECLARE
    old_data JSONB;
    new_data JSONB;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.revoked_at IS NOT DISTINCT FROM NEW.revoked_at
       AND OLD.user_id = NEW.user_id
       AND OLD.expires_at = NEW.expires_at THEN
        RETURN NEW;
    END IF;

    IF TG_OP <> 'INSERT' THEN
        old_data := to_jsonb(OLD) - 'token_hash';
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_data := to_jsonb(NEW) - 'token_hash';
    END IF;

    INSERT INTO audit_log (table_name, operation, old_data, new_data)
    VALUES (TG_TABLE_NAME, TG_OP, old_data, new_data);
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_web_sessions
    AFTER INSERT OR UPDATE OR DELETE ON web_sessions
    FOR EACH ROW EXECUTE FUNCTION audit_web_sessions_trigger_func();

INSERT INTO schema_migrations (version) VALUES (16) ON CONFLICT DO NOTHING;
