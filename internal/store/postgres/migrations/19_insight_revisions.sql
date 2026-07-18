-- Migration 19: add current revision state and baseline history snapshots.

ALTER TABLE insights
    ADD COLUMN revision INTEGER NOT NULL DEFAULT 1
        CONSTRAINT insights_revision_positive CHECK (revision > 0);

CREATE TABLE insight_revisions (
    insight_id UUID        NOT NULL REFERENCES insights(id) ON DELETE CASCADE,
    revision   INTEGER     NOT NULL
        CONSTRAINT insight_revisions_revision_positive CHECK (revision > 0),
    operation  TEXT        NOT NULL
        CONSTRAINT insight_revisions_operation_valid
            CHECK (operation IN ('baseline', 'create', 'update', 'delete', 'restore')),
    key         TEXT,
    content     TEXT        NOT NULL,
    tags        TEXT[]      NOT NULL,
    category    TEXT        NOT NULL
        CONSTRAINT insight_revisions_category_valid
            CHECK (category IN ('fact', 'decision', 'insight', 'preference', 'context', 'general')),
    source      TEXT        NOT NULL
        CONSTRAINT insight_revisions_source_valid
            CHECK (source IN ('user', 'repo', 'agent', 'command')),
    deleted_at  TIMESTAMPTZ,
    changed_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (insight_id, revision)
);

INSERT INTO insight_revisions (
    insight_id,
    revision,
    operation,
    key,
    content,
    tags,
    category,
    source,
    deleted_at,
    changed_by,
    changed_at
)
SELECT
    id,
    revision,
    'baseline',
    key,
    content,
    tags,
    category,
    source,
    deleted_at,
    NULL,
    COALESCE(deleted_at, updated_at)
FROM insights;

INSERT INTO schema_migrations (version) VALUES (19) ON CONFLICT DO NOTHING;
