-- +goose Up
CREATE TABLE issues (
    identifier              TEXT NOT NULL PRIMARY KEY,
    title                   TEXT NOT NULL DEFAULT '',
    assignee                TEXT NOT NULL DEFAULT '',
    team                    TEXT NOT NULL DEFAULT '',
    project_id              TEXT NOT NULL DEFAULT '',
    project_name            TEXT NOT NULL DEFAULT '',
    project_milestone_id    TEXT NOT NULL DEFAULT '',
    project_milestone_name  TEXT NOT NULL DEFAULT '',
    state_type              TEXT NOT NULL DEFAULT '',
    created_at              DATETIME,
    started_at              DATETIME,
    completed_at            DATETIME,
    archived_at             DATETIME,
    auto_archived_at        DATETIME,
    added_to_project_at     DATETIME,
    updated_at              DATETIME
);

CREATE INDEX idx_issues_updated_at   ON issues (updated_at);
CREATE INDEX idx_issues_completed_at ON issues (completed_at);

-- +goose Down
DROP TABLE issues;
