package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"forecasting/internal/item"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the concrete SQLite-backed item store.
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

// Upsert inserts or updates items. The unique key is (source, identifier).
func (s *Store) Upsert(ctx context.Context, items ...item.Item) error {
	const q = `
INSERT INTO items
    (source, identifier, title, assignee, team, project, status,
     created_at, started_at, completed_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source, identifier) DO UPDATE SET
    title        = excluded.title,
    assignee     = excluded.assignee,
    team         = excluded.team,
    project      = excluded.project,
    status       = excluded.status,
    created_at   = excluded.created_at,
    started_at   = excluded.started_at,
    completed_at = excluded.completed_at,
    updated_at   = excluded.updated_at`

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

	for _, it := range items {
		_, err := stmt.ExecContext(ctx,
			it.Source,
			it.Identifier,
			it.Title,
			it.Assignee,
			it.Team,
			it.Project,
			it.Status,
			nullTime(it.CreatedAt),
			nullTime(it.StartedAt),
			nullTime(it.CompletedAt),
			nullTime(it.UpdatedAt),
		)
		if err != nil {
			return fmt.Errorf("upsert %s/%s: %w", it.Source, it.Identifier, err)
		}
	}

	return tx.Commit()
}

// LatestUpdatedAt returns the maximum updated_at for items from source.
// Returns zero time if no items exist yet (signals a full fetch).
func (s *Store) LatestUpdatedAt(ctx context.Context, source string) (time.Time, error) {
	// Selecting the updated_at column directly (rather than MAX(updated_at))
	// keeps the result typed as DATETIME, which the sqlite driver requires
	// in order to scan it back into a time.Time instead of a string.
	row := s.db.QueryRowContext(ctx,
		`SELECT updated_at FROM items WHERE source = ? ORDER BY updated_at DESC LIMIT 1`, source)

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

// CompletedBetween returns completed items whose completed_at falls within
// [start, end] (inclusive). If assignees is non-empty, only items whose
// assignee is in that set are returned.
//
// Returned items have Source, Identifier, Title, Assignee, Team, Project,
// Status, StartedAt, CompletedAt, and UpdatedAt populated.
func (s *Store) CompletedBetween(ctx context.Context, source string, start, end time.Time, assignees []string) ([]item.Item, error) {
	q := `
SELECT source, identifier, title, assignee, team, project, status,
       started_at, completed_at, updated_at
FROM items
WHERE source = ?
  AND status = 'completed'
  AND completed_at >= ?
  AND completed_at <= ?`

	args := []any{source, start.UTC(), end.UTC()}

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

	var items []item.Item
	for rows.Next() {
		var it item.Item
		var startedAt, completedAt, updatedAt sql.NullTime
		if err := rows.Scan(
			&it.Source, &it.Identifier, &it.Title, &it.Assignee,
			&it.Team, &it.Project, &it.Status,
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
		items = append(items, it)
	}
	return items, rows.Err()
}

// InProgress returns items whose status is 'in_progress' and that have a
// non-NULL started_at. Results are ordered by started_at ascending.
func (s *Store) InProgress(ctx context.Context, source string) ([]item.Item, error) {
	const q = `
SELECT source, identifier, title, assignee, team, project, status, started_at
FROM items
WHERE source = ?
  AND status = 'in_progress'
  AND started_at IS NOT NULL
ORDER BY started_at ASC`

	rows, err := s.db.QueryContext(ctx, q, source)
	if err != nil {
		return nil, fmt.Errorf("InProgress: %w", err)
	}
	defer rows.Close()

	var items []item.Item
	for rows.Next() {
		var it item.Item
		var startedAt sql.NullTime
		if err := rows.Scan(
			&it.Source, &it.Identifier, &it.Title, &it.Assignee,
			&it.Team, &it.Project, &it.Status, &startedAt,
		); err != nil {
			return nil, fmt.Errorf("InProgress scan: %w", err)
		}
		if startedAt.Valid {
			it.StartedAt = startedAt.Time
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// nullTime converts a time.Time to sql.NullTime, treating zero as NULL.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
