package sqlite

import (
	"context"
	"database/sql"
	"embed"
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
	row := s.db.QueryRowContext(ctx,
		`SELECT MAX(updated_at) FROM items WHERE source = ?`, source)

	var ts sql.NullTime
	if err := row.Scan(&ts); err != nil {
		return time.Time{}, fmt.Errorf("latest updated_at: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

// nullTime converts a time.Time to sql.NullTime, treating zero as NULL.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
