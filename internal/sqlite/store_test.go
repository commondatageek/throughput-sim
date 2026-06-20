package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"forecasting/internal/linear"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestUpsertAndCompletedBetween(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	completed := linear.Issue{
		Identifier:  "ENG-1",
		Assignee:    "alice",
		StateType:   "completed",
		StartedAt:   mustParse(t, "2024-01-01T00:00:00Z"),
		CompletedAt: mustParse(t, "2024-01-05T00:00:00Z"),
		UpdatedAt:   mustParse(t, "2024-01-05T00:00:00Z"),
	}
	inProgress := linear.Issue{
		Identifier: "ENG-2",
		Assignee:   "bob",
		StateType:  "started",
		StartedAt:  mustParse(t, "2024-01-02T00:00:00Z"),
		UpdatedAt:  mustParse(t, "2024-01-02T00:00:00Z"),
	}

	if err := store.Upsert(ctx, completed, inProgress); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	start := mustParse(t, "2024-01-01T00:00:00Z")
	end := mustParse(t, "2024-01-10T00:00:00Z")
	got, err := store.CompletedBetween(ctx, start, end, nil)
	if err != nil {
		t.Fatalf("CompletedBetween: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "ENG-1" {
		t.Fatalf("CompletedBetween = %+v, want only ENG-1", got)
	}
}

func TestCompletedBetweenBoundary(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	atStart := linear.Issue{
		Identifier:  "ENG-AT-START",
		Assignee:    "alice",
		StateType:   "completed",
		StartedAt:   mustParse(t, "2024-01-01T00:00:00Z"),
		CompletedAt: mustParse(t, "2024-01-05T00:00:00Z"),
	}
	atEnd := linear.Issue{
		Identifier:  "ENG-AT-END",
		Assignee:    "alice",
		StateType:   "completed",
		StartedAt:   mustParse(t, "2024-01-01T00:00:00Z"),
		CompletedAt: mustParse(t, "2024-01-10T00:00:00Z"),
	}
	if err := store.Upsert(ctx, atStart, atEnd); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	start := mustParse(t, "2024-01-05T00:00:00Z")
	end := mustParse(t, "2024-01-10T00:00:00Z")
	got, err := store.CompletedBetween(ctx, start, end, nil)
	if err != nil {
		t.Fatalf("CompletedBetween: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "ENG-AT-START" {
		t.Fatalf("CompletedBetween = %+v, want only ENG-AT-START (start inclusive, end exclusive)", got)
	}
}

func TestCompletedBetweenAssigneeFilter(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	alice := linear.Issue{
		Identifier:  "ENG-ALICE",
		Assignee:    "alice",
		StateType:   "completed",
		CompletedAt: mustParse(t, "2024-01-05T00:00:00Z"),
	}
	bob := linear.Issue{
		Identifier:  "ENG-BOB",
		Assignee:    "bob",
		StateType:   "completed",
		CompletedAt: mustParse(t, "2024-01-05T00:00:00Z"),
	}
	if err := store.Upsert(ctx, alice, bob); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	start := mustParse(t, "2024-01-01T00:00:00Z")
	end := mustParse(t, "2024-01-10T00:00:00Z")
	got, err := store.CompletedBetween(ctx, start, end, []string{"alice"})
	if err != nil {
		t.Fatalf("CompletedBetween: %v", err)
	}
	if len(got) != 1 || got[0].Assignee != "alice" {
		t.Fatalf("CompletedBetween with assignee filter = %+v, want only alice", got)
	}
}

func TestInProgress(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	completed := linear.Issue{
		Identifier:  "ENG-1",
		StateType:   "completed",
		CompletedAt: mustParse(t, "2024-01-05T00:00:00Z"),
	}
	earlier := linear.Issue{
		Identifier: "ENG-2",
		StateType:  "started",
		StateName:  "In Review",
		StartedAt:  mustParse(t, "2024-01-01T00:00:00Z"),
	}
	later := linear.Issue{
		Identifier: "ENG-3",
		StateType:  "started",
		StartedAt:  mustParse(t, "2024-01-02T00:00:00Z"),
	}
	if err := store.Upsert(ctx, completed, later, earlier); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.InProgress(ctx)
	if err != nil {
		t.Fatalf("InProgress: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("InProgress returned %d issues, want 2 (got %+v)", len(got), got)
	}
	if got[0].Identifier != "ENG-2" || got[1].Identifier != "ENG-3" {
		t.Fatalf("InProgress order = [%s, %s], want [ENG-2, ENG-3] (started_at ASC)", got[0].Identifier, got[1].Identifier)
	}
	if got[0].StateName != "In Review" {
		t.Fatalf("InProgress StateName = %q, want %q", got[0].StateName, "In Review")
	}
}

func TestLatestUpdatedAt(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	zero, err := store.LatestUpdatedAt(ctx)
	if err != nil {
		t.Fatalf("LatestUpdatedAt on empty db: %v", err)
	}
	if !zero.IsZero() {
		t.Fatalf("LatestUpdatedAt on empty db = %v, want zero time", zero)
	}

	older := linear.Issue{Identifier: "ENG-1", UpdatedAt: mustParse(t, "2024-01-01T00:00:00Z")}
	newer := linear.Issue{Identifier: "ENG-2", UpdatedAt: mustParse(t, "2024-01-10T00:00:00Z")}
	if err := store.Upsert(ctx, older, newer); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.LatestUpdatedAt(ctx)
	if err != nil {
		t.Fatalf("LatestUpdatedAt: %v", err)
	}
	if !got.Equal(newer.UpdatedAt) {
		t.Fatalf("LatestUpdatedAt = %v, want %v", got, newer.UpdatedAt)
	}
}

func TestUpsertConflictUpdates(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, linear.Issue{Identifier: "ENG-1", Title: "first"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Upsert(ctx, linear.Issue{Identifier: "ENG-1", Title: "second"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE identifier = 'ENG-1'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count for ENG-1 = %d, want 1 (upsert should update, not duplicate)", count)
	}

	var title string
	if err := store.db.QueryRowContext(ctx, `SELECT title FROM issues WHERE identifier = 'ENG-1'`).Scan(&title); err != nil {
		t.Fatalf("title query: %v", err)
	}
	if title != "second" {
		t.Fatalf("title = %q, want %q", title, "second")
	}
}

func TestUpsertNullTimeRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, linear.Issue{Identifier: "ENG-1", StateType: "started", StartedAt: mustParse(t, "2024-01-01T00:00:00Z")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.InProgress(ctx)
	if err != nil {
		t.Fatalf("InProgress: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("InProgress returned %d issues, want 1", len(got))
	}
	if !got[0].CompletedAt.IsZero() {
		t.Fatalf("CompletedAt = %v, want zero time", got[0].CompletedAt)
	}
}
