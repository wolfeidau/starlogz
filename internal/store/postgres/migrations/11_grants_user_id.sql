-- Migration 11: Replace github_id with user_id on grants, scope prune to (user_id, client_id)

ALTER TABLE grants ADD COLUMN user_id UUID REFERENCES users(id) ON DELETE CASCADE;

UPDATE grants SET user_id = u.id FROM users u WHERE u.github_id = grants.github_id;

ALTER TABLE grants ALTER COLUMN user_id SET NOT NULL;

DROP INDEX IF EXISTS grants_github_id_idx;
ALTER TABLE grants DROP CONSTRAINT IF EXISTS grants_github_id_fkey;
ALTER TABLE grants DROP COLUMN github_id;

CREATE INDEX grants_user_id_client_id_idx ON grants (user_id, client_id);

INSERT INTO schema_migrations (version) VALUES (11) ON CONFLICT DO NOTHING;
