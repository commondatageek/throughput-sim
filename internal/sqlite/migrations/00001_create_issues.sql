-- +goose Up
CREATE TABLE issues (
    -- identifier/title/team_key/team_name/state_* are always present on a Linear issue.
    -- assignee and the project/milestone columns are genuinely optional and
    -- are stored as NULL when absent (see nullString in internal/sqlite).
    identifier              TEXT NOT NULL PRIMARY KEY,
    title                   TEXT NOT NULL DEFAULT '',
    assignee                TEXT,
    team_key                TEXT NOT NULL DEFAULT '',
    team_name               TEXT NOT NULL DEFAULT '',
    project_id              TEXT,
    project_name            TEXT,
    project_milestone_id    TEXT,
    project_milestone_name  TEXT,
    state_type              TEXT NOT NULL DEFAULT '',
    state_name              TEXT NOT NULL DEFAULT '',
    created_at              DATETIME,
    started_at              DATETIME,
    completed_at            DATETIME,
    canceled_at             DATETIME,
    archived_at             DATETIME,
    auto_archived_at        DATETIME,
    added_to_project_at     DATETIME,
    updated_at              DATETIME
);

CREATE INDEX idx_issues_team_key_updated_at ON issues (team_key, updated_at);
CREATE INDEX idx_issues_completed_at        ON issues (completed_at);

-- +goose Down
DROP TABLE issues;
