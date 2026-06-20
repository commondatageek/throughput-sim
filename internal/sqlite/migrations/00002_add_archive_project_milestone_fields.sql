-- +goose Up
ALTER TABLE items RENAME COLUMN project TO project_name;
ALTER TABLE items ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN milestone_id TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN milestone_name TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN archived_at DATETIME;
ALTER TABLE items ADD COLUMN auto_archived_at DATETIME;
ALTER TABLE items ADD COLUMN added_to_project_at DATETIME;

-- +goose Down
ALTER TABLE items DROP COLUMN added_to_project_at;
ALTER TABLE items DROP COLUMN auto_archived_at;
ALTER TABLE items DROP COLUMN archived_at;
ALTER TABLE items DROP COLUMN milestone_name;
ALTER TABLE items DROP COLUMN milestone_id;
ALTER TABLE items DROP COLUMN project_id;
ALTER TABLE items RENAME COLUMN project_name TO project;
