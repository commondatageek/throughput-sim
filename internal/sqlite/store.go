package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"forecasting/internal/linear"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the concrete SQLite-backed issue store.
// All SQL in this repo lives here.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and runs any pending migrations.
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

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		db.Close()
		return nil, fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Upsert inserts or updates issues. The unique key is identifier.
func (s *Store) Upsert(ctx context.Context, issues ...linear.Issue) error {
	const q = `
INSERT INTO issues
    (identifier, title, assignee, team, project_id, project_name,
     project_milestone_id, project_milestone_name, state_type, state_name,
     created_at, started_at, completed_at, archived_at, auto_archived_at,
     added_to_project_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(identifier) DO UPDATE SET
    title                  = excluded.title,
    assignee               = excluded.assignee,
    team                   = excluded.team,
    project_id             = excluded.project_id,
    project_name           = excluded.project_name,
    project_milestone_id   = excluded.project_milestone_id,
    project_milestone_name = excluded.project_milestone_name,
    state_type             = excluded.state_type,
    state_name             = excluded.state_name,
    created_at             = excluded.created_at,
    started_at             = excluded.started_at,
    completed_at           = excluded.completed_at,
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
			it.Assignee,
			it.Team,
			it.ProjectID,
			it.ProjectName,
			it.ProjectMilestoneID,
			it.ProjectMilestoneName,
			it.StateType,
			it.StateName,
			nullTime(it.CreatedAt),
			nullTime(it.StartedAt),
			nullTime(it.CompletedAt),
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

// LatestUpdatedAt returns the maximum updated_at across all issues.
// Returns zero time if no issues exist yet (signals a full fetch).
func (s *Store) LatestUpdatedAt(ctx context.Context) (time.Time, error) {
	// Selecting the updated_at column directly (rather than MAX(updated_at))
	// keeps the result typed as DATETIME, which the sqlite driver requires
	// in order to scan it back into a time.Time instead of a string.
	row := s.db.QueryRowContext(ctx,
		`SELECT updated_at FROM issues ORDER BY updated_at DESC LIMIT 1`)

	var ts sql.NullTime
	if err := row.Scan(&ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("latest updated_at: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

// CompletedBetween returns completed issues whose completed_at falls within
// [start, end) — start inclusive, end exclusive. If assignees is non-empty,
// only issues whose assignee is in that set are returned.
//
// Returned issues have Identifier, Title, Assignee, Team, ProjectName,
// StateType, StartedAt, CompletedAt, and UpdatedAt populated.
func (s *Store) CompletedBetween(ctx context.Context, start, end time.Time, assignees []string) ([]linear.Issue, error) {
	q := `
SELECT identifier, title, assignee, team, project_name, state_type,
       started_at, completed_at, updated_at
FROM issues
WHERE state_type = 'completed'
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

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("CompletedBetween: %w", err)
	}
	defer rows.Close()

	var issues []linear.Issue
	for rows.Next() {
		var it linear.Issue
		var startedAt, completedAt, updatedAt sql.NullTime
		if err := rows.Scan(
			&it.Identifier, &it.Title, &it.Assignee,
			&it.Team, &it.ProjectName, &it.StateType,
			&startedAt, &completedAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("CompletedBetween scan: %w", err)
		}
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
func (s *Store) InProgress(ctx context.Context) ([]linear.Issue, error) {
	const q = `
SELECT identifier, title, assignee, team, project_name, state_type, state_name, started_at
FROM issues
WHERE state_type = 'started'
  AND started_at IS NOT NULL
ORDER BY started_at ASC`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("InProgress: %w", err)
	}
	defer rows.Close()

	var issues []linear.Issue
	for rows.Next() {
		var it linear.Issue
		var startedAt sql.NullTime
		if err := rows.Scan(
			&it.Identifier, &it.Title, &it.Assignee,
			&it.Team, &it.ProjectName, &it.StateType, &it.StateName, &startedAt,
		); err != nil {
			return nil, fmt.Errorf("InProgress scan: %w", err)
		}
		if startedAt.Valid {
			it.StartedAt = startedAt.Time
		}
		issues = append(issues, it)
	}
	return issues, rows.Err()
}

// nullTime converts a time.Time to sql.NullTime, treating zero as NULL.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
