package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/commondatageek/delivery-forecast/internal/linear"

	_ "modernc.org/sqlite"
)

// Store is the concrete SQLite-backed issue store.
// All SQL in this repo lives here.
type Store struct {
	db *sql.DB
}

// schema is the full (and only) table definition. The store just replicates
// Linear's own data, so rebuilding the database from scratch is trivial and
// cheap; there's no history worth preserving across schema changes, so a
// single idempotent CREATE IF NOT EXISTS replaces versioned migrations.
const schema = `
CREATE TABLE IF NOT EXISTS issues (
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

CREATE INDEX IF NOT EXISTS idx_issues_team_key_updated_at ON issues (team_key, updated_at);
CREATE INDEX IF NOT EXISTS idx_issues_completed_at        ON issues (completed_at);
`

// Open opens (or creates) the SQLite database at path and ensures the schema
// exists. Since this always creates a missing file, it's meant for the
// ingest path (`linear sync`), which legitimately seeds a brand-new database.
// Read-only commands should use OpenExisting instead.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Enable WAL mode and foreign keys.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return &Store{db: db}, nil
}

// OpenExisting opens the SQLite database at path, first checking that the
// file already exists. Use this for read-only commands (sim, count, aging,
// cfd): since SQLite otherwise creates a missing file lazily, a typo'd or
// wrong -db path would silently open an empty database and proceed rather
// than failing.
func OpenExisting(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("database %q does not exist", path)
		}
		return nil, fmt.Errorf("stat db %q: %w", path, err)
	}
	return Open(path)
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Upsert inserts or updates issues. The unique key is identifier.
func (s *Store) Upsert(ctx context.Context, issues ...linear.Issue) error {
	const q = `
INSERT INTO issues
    (identifier, title, assignee, team_key, team_name, project_id, project_name,
     project_milestone_id, project_milestone_name, state_type, state_name,
     created_at, started_at, completed_at, canceled_at, archived_at, auto_archived_at,
     added_to_project_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(identifier) DO UPDATE SET
    title                  = excluded.title,
    assignee               = excluded.assignee,
    team_key               = excluded.team_key,
    team_name              = excluded.team_name,
    project_id             = excluded.project_id,
    project_name           = excluded.project_name,
    project_milestone_id   = excluded.project_milestone_id,
    project_milestone_name = excluded.project_milestone_name,
    state_type             = excluded.state_type,
    state_name             = excluded.state_name,
    created_at             = excluded.created_at,
    started_at             = excluded.started_at,
    completed_at           = excluded.completed_at,
    canceled_at            = excluded.canceled_at,
    archived_at            = excluded.archived_at,
    auto_archived_at       = excluded.auto_archived_at,
    added_to_project_at    = excluded.added_to_project_at,
    updated_at             = excluded.updated_at`

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return fmt.Errorf("prepare upsert: %w", err)
	}
	defer stmt.Close()

	for _, it := range issues {
		_, err := stmt.ExecContext(ctx,
			it.Identifier,
			it.Title,
			nullString(it.Assignee),
			it.TeamKey,
			it.TeamName,
			nullString(it.ProjectID),
			nullString(it.ProjectName),
			nullString(it.ProjectMilestoneID),
			nullString(it.ProjectMilestoneName),
			it.StateType,
			it.StateName,
			nullTime(it.CreatedAt),
			nullTime(it.StartedAt),
			nullTime(it.CompletedAt),
			nullTime(it.CanceledAt),
			nullTime(it.ArchivedAt),
			nullTime(it.AutoArchivedAt),
			nullTime(it.AddedToProjectAt),
			nullTime(it.UpdatedAt),
		)
		if err != nil {
			return fmt.Errorf("upsert %s: %w", it.Identifier, err)
		}
	}

	return tx.Commit()
}

// LatestUpdatedAtForTeam returns the maximum updated_at among issues for the
// given team key. Returns zero time if the team has no issues yet (signals a
// full fetch for that team).
func (s *Store) LatestUpdatedAtForTeam(ctx context.Context, teamKey string) (time.Time, error) {
	// Selecting the updated_at column directly (rather than MAX(updated_at))
	// keeps the result typed as DATETIME, which the sqlite driver requires
	// in order to scan it back into a time.Time instead of a string.
	row := s.db.QueryRowContext(ctx,
		`SELECT updated_at FROM issues WHERE team_key = ? ORDER BY updated_at DESC LIMIT 1`,
		teamKey)

	var ts sql.NullTime
	if err := row.Scan(&ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("latest updated_at for team %s: %w", teamKey, err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

// DistinctTeamKeys returns every non-empty team_key currently present in the
// store, ordered alphabetically.
func (s *Store) DistinctTeamKeys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT team_key FROM issues WHERE team_key <> '' ORDER BY team_key`)
	if err != nil {
		return nil, fmt.Errorf("distinct team keys: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("distinct team keys scan: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// CompletedBetween returns completed issues whose completed_at falls within
// [start, end) — start inclusive, end exclusive. If assignees is non-empty,
// only issues whose assignee is in that set are returned. Unassigned issues
// (NULL or empty assignee) are always excluded — throughput is attributed per
// engineer.
//
// The store now holds every issue (all states, assigned or not), so the
// state_type and assignee predicates here are load-bearing: they are the single
// chokepoint that keeps unstarted/canceled/unassigned issues out of the
// forecast. The `assignee <> ''` clause guards against an empty-string assignee
// slipping through `assignee IS NOT NULL`; today the writer normalizes "" to
// NULL (see nullString), but the reader shouldn't depend on that invariant.
//
// Returned issues have Identifier, Title, Assignee, Team, ProjectName,
// StateType, StartedAt, CompletedAt, and UpdatedAt populated.
func (s *Store) CompletedBetween(ctx context.Context, start, end time.Time, assignees []string, teamKeys []string) ([]linear.Issue, error) {
	q := `
SELECT identifier, title, assignee, team_name, project_name, state_type,
       started_at, completed_at, updated_at
FROM issues
WHERE state_type = 'completed'
  AND assignee IS NOT NULL
  AND assignee <> ''
  AND completed_at >= ?
  AND completed_at < ?`

	args := []any{start.UTC(), end.UTC()}

	if len(assignees) > 0 {
		placeholders := make([]byte, 0, len(assignees)*2)
		for i, a := range assignees {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args = append(args, a)
		}
		q += " AND assignee IN (" + string(placeholders) + ")"
	}

	if len(teamKeys) > 0 {
		placeholders := make([]byte, 0, len(teamKeys)*2)
		for i, k := range teamKeys {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args = append(args, k)
		}
		q += " AND team_key IN (" + string(placeholders) + ")"
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("CompletedBetween: %w", err)
	}
	defer rows.Close()

	var issues []linear.Issue
	for rows.Next() {
		var it linear.Issue
		var assignee, projectName sql.NullString
		var startedAt, completedAt, updatedAt sql.NullTime
		if err := rows.Scan(
			&it.Identifier, &it.Title, &assignee,
			&it.TeamName, &projectName, &it.StateType,
			&startedAt, &completedAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("CompletedBetween scan: %w", err)
		}
		it.Assignee = assignee.String
		it.ProjectName = projectName.String
		if startedAt.Valid {
			it.StartedAt = startedAt.Time
		}
		if completedAt.Valid {
			it.CompletedAt = completedAt.Time
		}
		if updatedAt.Valid {
			it.UpdatedAt = updatedAt.Time
		}
		issues = append(issues, it)
	}
	return issues, rows.Err()
}

// InProgress returns issues whose state_type is 'started' and that have a
// non-NULL started_at. Results are ordered by started_at ascending.
func (s *Store) InProgress(ctx context.Context, teamKeys []string) ([]linear.Issue, error) {
	q := `
SELECT identifier, title, assignee, team_name, project_name, state_type, state_name, started_at
FROM issues
WHERE state_type = 'started'
  AND started_at IS NOT NULL`

	var args []any
	if len(teamKeys) > 0 {
		placeholders := make([]byte, 0, len(teamKeys)*2)
		for i, k := range teamKeys {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args = append(args, k)
		}
		q += " AND team_key IN (" + string(placeholders) + ")"
	}
	q += "\nORDER BY started_at ASC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("InProgress: %w", err)
	}
	defer rows.Close()

	var issues []linear.Issue
	for rows.Next() {
		var it linear.Issue
		var assignee, projectName sql.NullString
		var startedAt sql.NullTime
		if err := rows.Scan(
			&it.Identifier, &it.Title, &assignee,
			&it.TeamName, &projectName, &it.StateType, &it.StateName, &startedAt,
		); err != nil {
			return nil, fmt.Errorf("InProgress scan: %w", err)
		}
		it.Assignee = assignee.String
		it.ProjectName = projectName.String
		if startedAt.Valid {
			it.StartedAt = startedAt.Time
		}
		issues = append(issues, it)
	}
	return issues, rows.Err()
}

// ProjectMilestoneCount is a count of issues grouped by team, project and
// milestone. Empty ProjectName / MilestoneName mean the issue had no project /
// no milestone.
type ProjectMilestoneCount struct {
	TeamKey       string
	TeamName      string
	ProjectName   string
	MilestoneName string
	Count         int
}

// NotCompletedCounts returns the number of issues that are not in a terminal
// state, grouped by team, project and milestone. Terminal states ('completed',
// 'canceled' and 'duplicate') are excluded, so the counts reflect
// outstanding/remaining work. Issues without a project or milestone come back
// with an empty ProjectName / MilestoneName for the caller to label. If
// teamKeys is non-empty, only issues belonging to those teams are counted.
func (s *Store) NotCompletedCounts(ctx context.Context, teamKeys []string) ([]ProjectMilestoneCount, error) {
	q := `
SELECT team_key,
       team_name,
       COALESCE(project_name, '')           AS project_name,
       COALESCE(project_milestone_name, '') AS milestone_name,
       COUNT(*)                             AS cnt
FROM issues
WHERE state_type NOT IN ('completed', 'canceled', 'duplicate')`

	var args []any
	if len(teamKeys) > 0 {
		placeholders := make([]byte, 0, len(teamKeys)*2)
		for i, k := range teamKeys {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args = append(args, k)
		}
		q += " AND team_key IN (" + string(placeholders) + ")"
	}

	q += `
GROUP BY team_key, team_name, project_name, milestone_name
ORDER BY project_name, milestone_name`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("NotCompletedCounts: %w", err)
	}
	defer rows.Close()

	var counts []ProjectMilestoneCount
	for rows.Next() {
		var c ProjectMilestoneCount
		if err := rows.Scan(&c.TeamKey, &c.TeamName, &c.ProjectName, &c.MilestoneName, &c.Count); err != nil {
			return nil, fmt.Errorf("NotCompletedCounts scan: %w", err)
		}
		counts = append(counts, c)
	}
	return counts, rows.Err()
}

// ProjectActivity is the most recent updated_at across all of a project's
// issues — including terminal (completed/canceled/duplicate) ones — so it
// reflects the last time the project was touched in any way. ProjectName is
// empty for issues with no project.
type ProjectActivity struct {
	TeamKey     string
	TeamName    string
	ProjectName string
	LastUpdated time.Time
}

// ProjectLastUpdated returns, per team and project, the most recent updated_at
// across ALL issues (no state filter), so callers can tell when a project was
// last touched even if the only recent change was to a completed issue. If
// teamKeys is non-empty, only those teams are considered.
func (s *Store) ProjectLastUpdated(ctx context.Context, teamKeys []string) ([]ProjectActivity, error) {
	q := `
SELECT team_key,
       team_name,
       COALESCE(project_name, '') AS project_name,
       MAX(updated_at)            AS max_updated_at
FROM issues`

	var args []any
	if len(teamKeys) > 0 {
		placeholders := make([]byte, 0, len(teamKeys)*2)
		for i, k := range teamKeys {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args = append(args, k)
		}
		q += " WHERE team_key IN (" + string(placeholders) + ")"
	}

	q += `
GROUP BY team_key, team_name, project_name`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("ProjectLastUpdated: %w", err)
	}
	defer rows.Close()

	var acts []ProjectActivity
	for rows.Next() {
		var a ProjectActivity
		// MAX(updated_at) is an aggregate expression, so the driver loses the
		// column's DATETIME affinity and returns the raw stored string rather
		// than a time.Time; parse it ourselves.
		var maxUpdated sql.NullString
		if err := rows.Scan(&a.TeamKey, &a.TeamName, &a.ProjectName, &maxUpdated); err != nil {
			return nil, fmt.Errorf("ProjectLastUpdated scan: %w", err)
		}
		if maxUpdated.Valid {
			t, err := parseSQLiteTime(maxUpdated.String)
			if err != nil {
				return nil, fmt.Errorf("ProjectLastUpdated parse %q: %w", maxUpdated.String, err)
			}
			a.LastUpdated = t
		}
		acts = append(acts, a)
	}
	return acts, rows.Err()
}

// ProjectMilestoneIssues returns all non-canceled, non-duplicate issues for
// the given project (optionally narrowed to one milestone within it). Completed
// issues are included so the caller can evaluate membership "as of" a given
// date using each issue's created_at and completed_at.
func (s *Store) ProjectMilestoneIssues(ctx context.Context, projectName, milestoneName string) ([]linear.Issue, error) {
	q := `
SELECT identifier, title, assignee, project_name, project_milestone_name,
       state_type, created_at, started_at, completed_at
FROM issues
WHERE project_name = ?
  AND state_type NOT IN ('canceled', 'duplicate')`

	args := []any{projectName}
	if milestoneName != "" {
		q += "\n  AND project_milestone_name = ?"
		args = append(args, milestoneName)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("ProjectMilestoneIssues: %w", err)
	}
	defer rows.Close()

	var issues []linear.Issue
	for rows.Next() {
		var it linear.Issue
		var assignee, proj, milestone sql.NullString
		var createdAt, startedAt, completedAt sql.NullTime
		if err := rows.Scan(
			&it.Identifier, &it.Title, &assignee,
			&proj, &milestone, &it.StateType,
			&createdAt, &startedAt, &completedAt,
		); err != nil {
			return nil, fmt.Errorf("ProjectMilestoneIssues scan: %w", err)
		}
		it.Assignee = assignee.String
		it.ProjectName = proj.String
		it.ProjectMilestoneName = milestone.String
		if createdAt.Valid {
			it.CreatedAt = createdAt.Time
		}
		if startedAt.Valid {
			it.StartedAt = startedAt.Time
		}
		if completedAt.Valid {
			it.CompletedAt = completedAt.Time
		}
		issues = append(issues, it)
	}
	return issues, rows.Err()
}

// CFDRow holds the timestamp columns needed to build a Cumulative Flow Diagram.
// Only the fields populated by CFDIssues are guaranteed to be set.
type CFDRow struct {
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	CanceledAt  time.Time
	StateType   string
}

// CFDIssues returns one CFDRow per issue (all issues, no state filter) so the
// caller can build cumulative arrival curves. If teamKeys is non-empty, only
// those teams are included. Rows are ordered by created_at ascending.
func (s *Store) CFDIssues(ctx context.Context, teamKeys []string) ([]CFDRow, error) {
	q := `
SELECT created_at, started_at, completed_at, canceled_at, state_type
FROM issues`

	var args []any
	if len(teamKeys) > 0 {
		placeholders := make([]byte, 0, len(teamKeys)*2)
		for i, k := range teamKeys {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args = append(args, k)
		}
		q += " WHERE team_key IN (" + string(placeholders) + ")"
	}
	q += "\nORDER BY created_at ASC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("CFDIssues: %w", err)
	}
	defer rows.Close()

	var out []CFDRow
	for rows.Next() {
		var createdAt, startedAt, completedAt, canceledAt sql.NullTime
		var stateType string
		if err := rows.Scan(&createdAt, &startedAt, &completedAt, &canceledAt, &stateType); err != nil {
			return nil, fmt.Errorf("CFDIssues scan: %w", err)
		}
		row := CFDRow{StateType: stateType}
		if createdAt.Valid {
			row.CreatedAt = createdAt.Time
		}
		if startedAt.Valid {
			row.StartedAt = startedAt.Time
		}
		if completedAt.Valid {
			row.CompletedAt = completedAt.Time
		}
		if canceledAt.Valid {
			row.CanceledAt = canceledAt.Time
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// sqliteTimeLayouts are the timestamp formats modernc.org/sqlite may have
// written DATETIME values in. The first matches how the driver binds a
// time.Time; the rest are fallbacks.
var sqliteTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999 -0700 MST", // time.Time.String(), how the driver binds time values
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseSQLiteTime parses a DATETIME string read back from SQLite, trying the
// layouts the driver may have stored.
func parseSQLiteTime(s string) (time.Time, error) {
	for _, layout := range sqliteTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format")
}

// nullTime converts a time.Time to sql.NullTime, treating zero as NULL.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// nullString converts a string to sql.NullString, treating "" as NULL. Used
// for the optional columns (assignee, project, milestone) so that absent data
// is stored as NULL rather than an empty string.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
