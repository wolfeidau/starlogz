-- Migration 1: Initial schema — users, orgs, membership, projects, facts

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER     PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
    id         UUID        PRIMARY KEY DEFAULT uuidv7(),
    github_id  BIGINT      NOT NULL,
    email      TEXT        NOT NULL,
    login      TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_github_id_unique UNIQUE (github_id)
);

CREATE TABLE IF NOT EXISTS orgs (
    id         UUID        PRIMARY KEY DEFAULT uuidv7(),
    slug       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    kind       TEXT        NOT NULL CHECK (kind IN ('personal', 'shared')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only shared org slugs are unique; personal org slugs are display-only and resolved via user ID.
CREATE UNIQUE INDEX IF NOT EXISTS orgs_shared_slug_unique ON orgs (slug) WHERE kind = 'shared';

CREATE TABLE IF NOT EXISTS org_members (
    org_id    UUID        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id   UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      TEXT        NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

CREATE INDEX IF NOT EXISTS org_members_user_idx ON org_members (user_id);

CREATE TABLE IF NOT EXISTS projects (
    id         UUID        PRIMARY KEY DEFAULT uuidv7(),
    org_id     UUID        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    slug       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    created_by UUID        NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT projects_org_slug_unique UNIQUE (org_id, slug)
);

CREATE TABLE IF NOT EXISTS facts (
    id            UUID        PRIMARY KEY DEFAULT uuidv7(),
    project_id    UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    key           TEXT,
    content       TEXT        NOT NULL,
    tags          TEXT[]      NOT NULL DEFAULT '{}',
    source_type   TEXT        NOT NULL CHECK (source_type IN ('human', 'agent')),
    created_by    UUID        NOT NULL REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ,
    search_vector TSVECTOR    GENERATED ALWAYS AS (to_tsvector('english', content)) STORED
);

CREATE UNIQUE INDEX IF NOT EXISTS facts_project_key_live
    ON facts (project_id, key)
    WHERE key IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS facts_project_active ON facts (project_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS facts_search         ON facts USING GIN (search_vector);
CREATE INDEX IF NOT EXISTS facts_tags           ON facts USING GIN (tags);

INSERT INTO schema_migrations (version) VALUES (1) ON CONFLICT DO NOTHING;
