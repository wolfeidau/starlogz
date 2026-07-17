-- starlogz:concurrent-index insights_project_updated_live
CREATE INDEX CONCURRENTLY insights_project_updated_live
    ON insights (project_id, updated_at DESC, id DESC)
    WHERE deleted_at IS NULL;
