package sqlite

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/commondatageek/delivery-forecast/internal/linear"
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
	got, err := store.CompletedBetween(ctx, start, end, nil, nil)
	if err != nil {
		t.Fatalf("CompletedBetween: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "ENG-1" {
		t.Fatalf("CompletedBetween = %+v, want only ENG-1", got)
	}
}

func TestNotCompletedCounts(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	issues := []linear.Issue{
		// Apollo / Beta: two not-completed.
		{Identifier: "ENG-1", TeamKey: "ENG", StateType: "started", ProjectName: "Apollo", ProjectMilestoneName: "Beta"},
		{Identifier: "ENG-2", TeamKey: "ENG", StateType: "backlog", ProjectName: "Apollo", ProjectMilestoneName: "Beta"},
		// Apollo with no milestone.
		{Identifier: "ENG-3", TeamKey: "ENG", StateType: "unstarted", ProjectName: "Apollo"},
		// No project, no milestone; different team.
		{Identifier: "DES-1", TeamKey: "DES", StateType: "started"},
		// Terminal states are excluded.
		{Identifier: "ENG-5", TeamKey: "ENG", StateType: "completed", ProjectName: "Apollo", ProjectMilestoneName: "Beta"},
		{Identifier: "ENG-6", TeamKey: "ENG", StateType: "canceled", ProjectName: "Apollo"},
		{Identifier: "ENG-7", TeamKey: "ENG", StateType: "duplicate", ProjectName: "Apollo"},
	}
	if err := store.Upsert(ctx, issues...); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.NotCompletedCounts(ctx, nil)
	if err != nil {
		t.Fatalf("NotCompletedCounts: %v", err)
	}

	want := []ProjectMilestoneCount{
		{TeamKey: "DES", ProjectName: "", MilestoneName: "", Count: 1},
		{TeamKey: "ENG", ProjectName: "Apollo", MilestoneName: "", Count: 1},
		{TeamKey: "ENG", ProjectName: "Apollo", MilestoneName: "Beta", Count: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("NotCompletedCounts = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// With a team filter, only that team's issues are counted.
	gotTeam, err := store.NotCompletedCounts(ctx, []string{"ENG"})
	if err != nil {
		t.Fatalf("NotCompletedCounts(ENG): %v", err)
	}
	wantTeam := []ProjectMilestoneCount{
		{TeamKey: "ENG", ProjectName: "Apollo", MilestoneName: "", Count: 1},
		{TeamKey: "ENG", ProjectName: "Apollo", MilestoneName: "Beta", Count: 2},
	}
	if len(gotTeam) != len(wantTeam) {
		t.Fatalf("NotCompletedCounts(ENG) = %+v, want %+v", gotTeam, wantTeam)
	}
	for i := range wantTeam {
		if gotTeam[i] != wantTeam[i] {
			t.Errorf("ENG row %d = %+v, want %+v", i, gotTeam[i], wantTeam[i])
		}
	}
}

func TestProjectLastUpdated(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	older := mustParse(t, "2024-01-01T00:00:00Z")
	newer := mustParse(t, "2024-03-15T12:30:00Z")

	issues := []linear.Issue{
		// Apollo's newest touch is a completed (terminal) issue.
		{Identifier: "ENG-1", TeamKey: "ENG", StateType: "started", ProjectName: "Apollo", UpdatedAt: older},
		{Identifier: "ENG-2", TeamKey: "ENG", StateType: "completed", ProjectName: "Apollo", UpdatedAt: newer},
		// An issue with no project.
		{Identifier: "ENG-3", TeamKey: "ENG", StateType: "started", UpdatedAt: older},
	}
	if err := store.Upsert(ctx, issues...); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.ProjectLastUpdated(ctx, nil)
	if err != nil {
		t.Fatalf("ProjectLastUpdated: %v", err)
	}

	last := make(map[string]time.Time)
	for _, a := range got {
		last[a.ProjectName] = a.LastUpdated
	}
	// Terminal issues must count toward "last touched".
	if !last["Apollo"].Equal(newer) {
		t.Errorf("Apollo last updated = %v, want %v", last["Apollo"], newer)
	}
	if !last[""].Equal(older) {
		t.Errorf("(no project) last updated = %v, want %v", last[""], older)
	}
}

func TestCompletedBetweenExcludesUnassigned(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	assigned := linear.Issue{
		Identifier:  "ENG-1",
		Assignee:    "alice",
		StateType:   "completed",
		CompletedAt: mustParse(t, "2024-01-05T00:00:00Z"),
	}
	unassigned := linear.Issue{
		Identifier:  "ENG-2",
		StateType:   "completed",
		CompletedAt: mustParse(t, "2024-01-06T00:00:00Z"),
	}
	if err := store.Upsert(ctx, assigned, unassigned); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	start := mustParse(t, "2024-01-01T00:00:00Z")
	end := mustParse(t, "2024-01-10T00:00:00Z")
	got, err := store.CompletedBetween(ctx, start, end, nil, nil)
	if err != nil {
		t.Fatalf("CompletedBetween: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "ENG-1" {
		t.Fatalf("CompletedBetween = %+v, want only the assigned ENG-1", got)
	}
}

func TestCompletedBetweenExcludesEmptyAssignee(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Upsert normalizes "" to NULL via nullString, so to exercise the
	// `assignee <> ''` guard we have to write an empty-string assignee directly,
	// simulating a row that bypassed that normalization.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO issues (identifier, assignee, state_type, completed_at)
		 VALUES ('ENG-EMPTY', '', 'completed', ?)`,
		mustParse(t, "2024-01-05T00:00:00Z"),
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	start := mustParse(t, "2024-01-01T00:00:00Z")
	end := mustParse(t, "2024-01-10T00:00:00Z")
	got, err := store.CompletedBetween(ctx, start, end, nil, nil)
	if err != nil {
		t.Fatalf("CompletedBetween: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("CompletedBetween = %+v, want none (empty-string assignee excluded)", got)
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
	got, err := store.CompletedBetween(ctx, start, end, nil, nil)
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
	got, err := store.CompletedBetween(ctx, start, end, []string{"alice"}, nil)
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

	got, err := store.InProgress(ctx, nil)
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

func TestLatestUpdatedAtForTeam(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	zero, err := store.LatestUpdatedAtForTeam(ctx, "ENG")
	if err != nil {
		t.Fatalf("LatestUpdatedAtForTeam on empty db: %v", err)
	}
	if !zero.IsZero() {
		t.Fatalf("LatestUpdatedAtForTeam on empty db = %v, want zero time", zero)
	}

	older := linear.Issue{Identifier: "ENG-1", TeamKey: "ENG", UpdatedAt: mustParse(t, "2024-01-01T00:00:00Z")}
	newer := linear.Issue{Identifier: "ENG-2", TeamKey: "ENG", UpdatedAt: mustParse(t, "2024-01-10T00:00:00Z")}
	otherTeam := linear.Issue{Identifier: "DATA-1", TeamKey: "DATA", UpdatedAt: mustParse(t, "2024-06-01T00:00:00Z")}
	if err := store.Upsert(ctx, older, newer, otherTeam); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.LatestUpdatedAtForTeam(ctx, "ENG")
	if err != nil {
		t.Fatalf("LatestUpdatedAtForTeam: %v", err)
	}
	if !got.Equal(newer.UpdatedAt) {
		t.Fatalf("LatestUpdatedAtForTeam(ENG) = %v, want %v (other teams' watermarks must not leak in)", got, newer.UpdatedAt)
	}
}

func TestDistinctTeamKeys(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx,
		linear.Issue{Identifier: "ENG-1", TeamKey: "ENG"},
		linear.Issue{Identifier: "DATA-1", TeamKey: "DATA"},
		linear.Issue{Identifier: "ENG-2", TeamKey: "ENG"},
		linear.Issue{Identifier: "NOTEAM-1", TeamKey: ""},
	); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.DistinctTeamKeys(ctx)
	if err != nil {
		t.Fatalf("DistinctTeamKeys: %v", err)
	}
	want := []string{"DATA", "ENG"}
	if !slices.Equal(got, want) {
		t.Fatalf("DistinctTeamKeys = %v, want %v", got, want)
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

	got, err := store.InProgress(ctx, nil)
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

func TestProjectMilestoneIssues(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	issues := []linear.Issue{
		{Identifier: "ENG-1", ProjectName: "Apollo", ProjectMilestoneName: "v1.0", StateType: "started",
			CreatedAt: mustParse(t, "2024-01-01T00:00:00Z")},
		{Identifier: "ENG-2", ProjectName: "Apollo", ProjectMilestoneName: "v1.0", StateType: "completed",
			CreatedAt: mustParse(t, "2024-01-02T00:00:00Z"), CompletedAt: mustParse(t, "2024-01-10T00:00:00Z")},
		{Identifier: "ENG-3", ProjectName: "Apollo", ProjectMilestoneName: "v1.0", StateType: "canceled"},
		{Identifier: "ENG-4", ProjectName: "Apollo", ProjectMilestoneName: "v1.0", StateType: "duplicate"},
		{Identifier: "ENG-5", ProjectName: "Apollo", ProjectMilestoneName: "v2.0", StateType: "started"},
		{Identifier: "ENG-6", ProjectName: "Zeus", ProjectMilestoneName: "v1.0", StateType: "started"},
	}
	if err := store.Upsert(ctx, issues...); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Project-only: all non-canceled/dup Apollo issues across both milestones.
	got, err := store.ProjectMilestoneIssues(ctx, "Apollo", "")
	if err != nil {
		t.Fatalf("ProjectMilestoneIssues(Apollo, \"\"): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("project-only = %d issues, want 3: %+v", len(got), got)
	}
	for _, it := range got {
		if it.StateType == "canceled" || it.StateType == "duplicate" {
			t.Errorf("excluded state_type returned: %+v", it)
		}
		if it.ProjectName != "Apollo" {
			t.Errorf("wrong project: %+v", it)
		}
	}

	// Milestone-scoped: only v1.0, excludes canceled/dup.
	gotMS, err := store.ProjectMilestoneIssues(ctx, "Apollo", "v1.0")
	if err != nil {
		t.Fatalf("ProjectMilestoneIssues(Apollo, v1.0): %v", err)
	}
	if len(gotMS) != 2 {
		t.Fatalf("milestone filter = %d issues, want 2: %+v", len(gotMS), gotMS)
	}
	ids := []string{gotMS[0].Identifier, gotMS[1].Identifier}
	if !slices.Contains(ids, "ENG-1") || !slices.Contains(ids, "ENG-2") {
		t.Errorf("milestone filter ids = %v, want ENG-1 and ENG-2", ids)
	}
	// completed_at round-trips.
	for _, it := range gotMS {
		if it.Identifier == "ENG-2" && it.CompletedAt.IsZero() {
			t.Errorf("ENG-2.CompletedAt is zero, want non-zero")
		}
	}
}

func TestCFDIssues(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	day := func(s string) time.Time { return mustParse(t, s+"T00:00:00Z") }

	issues := []linear.Issue{
		// Normal completed issue: created → started → completed.
		{Identifier: "ENG-1", TeamKey: "ENG", StateType: "completed",
			CreatedAt: day("2024-01-01"), StartedAt: day("2024-01-03"), CompletedAt: day("2024-01-08")},
		// Canceled after starting.
		{Identifier: "ENG-2", TeamKey: "ENG", StateType: "canceled",
			CreatedAt: day("2024-01-02"), StartedAt: day("2024-01-04"), CanceledAt: day("2024-01-07")},
		// Canceled from backlog (no started_at).
		{Identifier: "ENG-3", TeamKey: "ENG", StateType: "canceled",
			CreatedAt: day("2024-01-03"), CanceledAt: day("2024-01-06")},
		// Still active (no exit timestamp).
		{Identifier: "ENG-4", TeamKey: "ENG", StateType: "started",
			CreatedAt: day("2024-01-04"), StartedAt: day("2024-01-05")},
		// Different team — excluded when filtering.
		{Identifier: "DATA-1", TeamKey: "DATA", StateType: "completed",
			CreatedAt: day("2024-01-01"), CompletedAt: day("2024-01-05")},
	}
	if err := store.Upsert(ctx, issues...); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// All teams: 5 rows.
	all, err := store.CFDIssues(ctx, nil)
	if err != nil {
		t.Fatalf("CFDIssues(nil): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("CFDIssues(nil) = %d rows, want 5", len(all))
	}

	// Team filter: 4 ENG rows, DATA excluded.
	eng, err := store.CFDIssues(ctx, []string{"ENG"})
	if err != nil {
		t.Fatalf("CFDIssues(ENG): %v", err)
	}
	if len(eng) != 4 {
		t.Fatalf("CFDIssues(ENG) = %d rows, want 4", len(eng))
	}

	// Spot-check timestamps round-trip correctly.
	byID := make(map[string]CFDRow)
	for _, r := range eng {
		// Identify by CreatedAt (unique in our fixture).
		for _, orig := range issues {
			if orig.TeamKey == "ENG" && orig.CreatedAt.Equal(r.CreatedAt) {
				byID[orig.Identifier] = r
			}
		}
	}

	eng1 := byID["ENG-1"]
	if !eng1.CompletedAt.Equal(day("2024-01-08")) {
		t.Errorf("ENG-1 CompletedAt = %v, want 2024-01-08", eng1.CompletedAt)
	}
	eng2 := byID["ENG-2"]
	if !eng2.CanceledAt.Equal(day("2024-01-07")) {
		t.Errorf("ENG-2 CanceledAt = %v, want 2024-01-07", eng2.CanceledAt)
	}
	eng3 := byID["ENG-3"]
	if !eng3.StartedAt.IsZero() {
		t.Errorf("ENG-3 StartedAt = %v, want zero (canceled from backlog)", eng3.StartedAt)
	}
	if !eng3.CanceledAt.Equal(day("2024-01-06")) {
		t.Errorf("ENG-3 CanceledAt = %v, want 2024-01-06", eng3.CanceledAt)
	}
	eng4 := byID["ENG-4"]
	if !eng4.CompletedAt.IsZero() || !eng4.CanceledAt.IsZero() {
		t.Errorf("ENG-4 should have no exit timestamps: CompletedAt=%v CanceledAt=%v", eng4.CompletedAt, eng4.CanceledAt)
	}
}

func TestUpsertStoresAbsentOptionalFieldsAsNull(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// An unassigned issue with no project/milestone — every optional field empty.
	if err := store.Upsert(ctx, linear.Issue{Identifier: "ENG-1", StateType: "started", StartedAt: mustParse(t, "2024-01-01T00:00:00Z")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	const q = `SELECT
		assignee IS NULL, project_id IS NULL, project_name IS NULL,
		project_milestone_id IS NULL, project_milestone_name IS NULL
	FROM issues WHERE identifier = 'ENG-1'`

	var assigneeNull, projIDNull, projNameNull, msIDNull, msNameNull bool
	if err := store.db.QueryRowContext(ctx, q).Scan(
		&assigneeNull, &projIDNull, &projNameNull, &msIDNull, &msNameNull,
	); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !(assigneeNull && projIDNull && projNameNull && msIDNull && msNameNull) {
		t.Fatalf("optional fields stored as NULL = assignee:%v proj_id:%v proj_name:%v ms_id:%v ms_name:%v, want all true",
			assigneeNull, projIDNull, projNameNull, msIDNull, msNameNull)
	}

	// Round-trips back to empty strings on the Go side.
	got, err := store.InProgress(ctx, nil)
	if err != nil {
		t.Fatalf("InProgress: %v", err)
	}
	if got[0].Assignee != "" || got[0].ProjectName != "" {
		t.Fatalf("NULL did not round-trip to empty string: %+v", got[0])
	}
}
