-- +goose Up
CREATE TABLE items (
    source       TEXT NOT NULL,
    identifier   TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    assignee     TEXT NOT NULL DEFAULT '',
    team         TEXT NOT NULL DEFAULT '',
    project      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT '',
    created_at   DATETIME,
    started_at   DATETIME,
    completed_at DATETIME,
    updated_at   DATETIME,
    PRIMARY KEY (source, identifier)
);

CREATE INDEX idx_items_source_updated_at ON items (source, updated_at);
CREATE INDEX idx_items_completed_at ON items (completed_at);

-- +goose Down
DROP TABLE items;
