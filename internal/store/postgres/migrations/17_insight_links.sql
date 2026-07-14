-- Migration 17: project-local relationships extracted from insight content.

CREATE TABLE insight_links (
    source_insight_id UUID        NOT NULL REFERENCES insights(id) ON DELETE CASCADE,
    target_key        TEXT        NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_insight_id, target_key)
);

CREATE INDEX insight_links_target_key_idx
    ON insight_links (target_key, source_insight_id);

CREATE TRIGGER audit_insight_links
    AFTER INSERT OR UPDATE OR DELETE ON insight_links
    FOR EACH ROW EXECUTE FUNCTION audit_trigger_func();

INSERT INTO schema_migrations (version) VALUES (17) ON CONFLICT DO NOTHING;
