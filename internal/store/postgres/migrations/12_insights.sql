-- Migration 12: rename facts → insights, add category/source, drop source_type

ALTER TABLE facts RENAME TO insights;

ALTER INDEX facts_project_key_live RENAME TO insights_project_key_live;
ALTER INDEX facts_project_active   RENAME TO insights_project_active;
ALTER INDEX facts_search            RENAME TO insights_search;
ALTER INDEX facts_tags              RENAME TO insights_tags;

ALTER TABLE insights
    ADD COLUMN category TEXT NOT NULL DEFAULT 'general'
        CHECK (category IN ('fact', 'decision', 'insight', 'preference', 'context', 'general')),
    ADD COLUMN source TEXT NOT NULL DEFAULT 'user'
        CHECK (source IN ('user', 'repo', 'agent', 'command'));

ALTER TABLE insights DROP COLUMN source_type;

ALTER TRIGGER audit_facts ON insights RENAME TO audit_insights;

INSERT INTO schema_migrations (version) VALUES (12) ON CONFLICT DO NOTHING;
