-- Migration 8: Personal org slugs are display-only; only shared org slugs must be unique.
--
-- The global UNIQUE constraint on orgs.slug prevents two users from sharing
-- the same GitHub login slug in their personal orgs, and requires collision
-- fallback logic in UpsertUser. Personal orgs are always resolved from the
-- user's JWT sub (not by slug), so uniqueness adds no correctness guarantee.
--
-- DROP CONSTRAINT IF EXISTS handles both cases:
--   - Existing databases created before this migration (constraint present).
--   - Fresh databases initialised from the updated migration 1 (constraint absent).

ALTER TABLE orgs DROP CONSTRAINT IF EXISTS orgs_slug_key;

CREATE UNIQUE INDEX IF NOT EXISTS orgs_shared_slug_unique
    ON orgs (slug)
    WHERE kind = 'shared';

INSERT INTO schema_migrations (version) VALUES (8) ON CONFLICT DO NOTHING;
