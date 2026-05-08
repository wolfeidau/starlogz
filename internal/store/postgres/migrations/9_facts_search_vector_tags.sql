-- Migration 9: include tag names in facts full-text search vector
--
-- array_to_string is STABLE (conservative default), but for text[] it is
-- deterministic; wrapping it in an IMMUTABLE function lets us use it inside
-- a GENERATED ALWAYS AS column.

CREATE OR REPLACE FUNCTION immutable_text_array_to_string(text[])
    RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS
    $$ SELECT array_to_string($1, ' ') $$;

ALTER TABLE facts
    DROP COLUMN search_vector;

ALTER TABLE facts
    ADD COLUMN search_vector TSVECTOR
        GENERATED ALWAYS AS (
            to_tsvector('english', content) ||
            to_tsvector('english', immutable_text_array_to_string(tags))
        ) STORED;

CREATE INDEX IF NOT EXISTS facts_search ON facts USING GIN (search_vector);

INSERT INTO schema_migrations (version) VALUES (9) ON CONFLICT DO NOTHING;
