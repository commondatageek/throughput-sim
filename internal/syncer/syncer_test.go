package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
)

type fetchCall struct {
	since time.Time
	teams []string
}

// stubClient is a test double for the client interface: it records every
// Fetch call and returns canned issues/errors per team key.
type stubClient struct {
	fetchCalls []fetchCall
	issuesFor  map[string][]linear.Issue
	fetchErr   map[string]error
	teams      []linear.Team
	listErr    error
}

func (s *stubClient) Fetch(ctx context.Context, since time.Time, teamKeys []string) ([]linear.Issue, error) {
	s.fetchCalls = append(s.fetchCalls, fetchCall{since: since, teams: teamKeys})
	key := teamKeys[0]
	if err, ok := s.fetchErr[key]; ok {
		return nil, err
	}
	return s.issuesFor[key], nil
}

func (s *stubClient) ListTeams(ctx context.Context) ([]linear.Team, error) {
	return s.teams, s.listErr
}

func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRun_TeamsAndAllTeamsMutuallyExclusive(t *testing.T) {
	store := openTestStore(t)
	err := Run(context.Background(), &stubClient{}, store, Options{
		Teams:    linear.KeyList{"ENG"},
		AllTeams: true,
	})
	if err == nil {
		t.Fatal("Run = nil, want mutual-exclusion error")
	}
}

func TestRun_EmptyDBNoCandidates(t *testing.T) {
	store := openTestStore(t)
	err := Run(context.Background(), &stubClient{}, store, Options{})
	if err == nil {
		t.Fatal("Run = nil, want error telling the caller to specify -teams")
	}
}

func TestRun_EmptyDBWithTeams_FullSync(t *testing.T) {
	store := openTestStore(t)
	sc := &stubClient{issuesFor: map[string][]linear.Issue{
		"ENG": {{Identifier: "ENG-1", TeamKey: "ENG", StateType: "completed"}},
	}}
	if err := Run(context.Background(), sc, store, Options{Teams: linear.KeyList{"ENG"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sc.fetchCalls) != 1 {
		t.Fatalf("fetch calls = %d, want 1", len(sc.fetchCalls))
	}
	if !sc.fetchCalls[0].since.IsZero() {
		t.Fatalf("since = %v, want zero (full sync)", sc.fetchCalls[0].since)
	}

	keys, err := store.DistinctTeamKeys(context.Background())
	if err != nil {
		t.Fatalf("DistinctTeamKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "ENG" {
		t.Fatalf("DistinctTeamKeys = %v, want [ENG]", keys)
	}
}

func TestRun_ExistingTeamIncremental_UsesWatermark(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	watermark := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	seed := linear.Issue{
		Identifier: "ENG-1", TeamKey: "ENG", StateType: "completed",
		UpdatedAt: watermark,
	}
	if err := store.Upsert(ctx, seed); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	sc := &stubClient{issuesFor: map[string][]linear.Issue{
		"ENG": {{Identifier: "ENG-2", TeamKey: "ENG", StateType: "started"}},
	}}
	if err := Run(ctx, sc, store, Options{Teams: linear.KeyList{"ENG"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sc.fetchCalls) != 1 {
		t.Fatalf("fetch calls = %d, want 1", len(sc.fetchCalls))
	}
	if !sc.fetchCalls[0].since.Equal(watermark) {
		t.Fatalf("since = %v, want watermark %v", sc.fetchCalls[0].since, watermark)
	}
}

func TestRun_ExistingTeamFullReload_IgnoresWatermark(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	watermark := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	seed := linear.Issue{
		Identifier: "ENG-1", TeamKey: "ENG", StateType: "completed",
		UpdatedAt: watermark,
	}
	if err := store.Upsert(ctx, seed); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	sc := &stubClient{issuesFor: map[string][]linear.Issue{"ENG": nil}}
	if err := Run(ctx, sc, store, Options{Teams: linear.KeyList{"ENG"}, FullReload: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sc.fetchCalls) != 1 {
		t.Fatalf("fetch calls = %d, want 1", len(sc.fetchCalls))
	}
	if !sc.fetchCalls[0].since.IsZero() {
		t.Fatalf("since = %v, want zero despite existing watermark (-full-reload)", sc.fetchCalls[0].since)
	}
}

func TestRun_AllTeams_CandidatesFromListTeams(t *testing.T) {
	store := openTestStore(t)
	sc := &stubClient{
		teams: []linear.Team{{Key: "ENG", Name: "Engineering"}, {Key: "DATA", Name: "Data"}},
		issuesFor: map[string][]linear.Issue{
			"ENG":  {{Identifier: "ENG-1", TeamKey: "ENG", StateType: "completed"}},
			"DATA": {{Identifier: "DATA-1", TeamKey: "DATA", StateType: "completed"}},
		},
	}
	if err := Run(context.Background(), sc, store, Options{AllTeams: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sc.fetchCalls) != 2 {
		t.Fatalf("fetch calls = %d, want 2", len(sc.fetchCalls))
	}
	gotTeams := map[string]bool{}
	for _, c := range sc.fetchCalls {
		gotTeams[c.teams[0]] = true
	}
	if !gotTeams["ENG"] || !gotTeams["DATA"] {
		t.Fatalf("fetched teams = %v, want ENG and DATA", gotTeams)
	}
}

func TestRun_NoCandidates_SyncsExistingTeams(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx,
		linear.Issue{Identifier: "ENG-1", TeamKey: "ENG", StateType: "completed", UpdatedAt: time.Now()},
		linear.Issue{Identifier: "DATA-1", TeamKey: "DATA", StateType: "completed", UpdatedAt: time.Now()},
	); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	sc := &stubClient{issuesFor: map[string][]linear.Issue{"ENG": nil, "DATA": nil}}
	if err := Run(ctx, sc, store, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sc.fetchCalls) != 2 {
		t.Fatalf("fetch calls = %d, want 2 (one per existing team)", len(sc.fetchCalls))
	}
}

func TestRun_FetchErrorMidLoop_PriorTeamCommitted(t *testing.T) {
	store := openTestStore(t)
	sc := &stubClient{
		issuesFor: map[string][]linear.Issue{
			"ENG": {{Identifier: "ENG-1", TeamKey: "ENG", StateType: "completed"}},
		},
		fetchErr: map[string]error{
			"DATA": errors.New("boom"),
		},
	}
	err := Run(context.Background(), sc, store, Options{Teams: linear.KeyList{"ENG", "DATA"}})
	if err == nil {
		t.Fatal("Run = nil, want error from DATA fetch failure")
	}

	keys, kerr := store.DistinctTeamKeys(context.Background())
	if kerr != nil {
		t.Fatalf("DistinctTeamKeys: %v", kerr)
	}
	if len(keys) != 1 || keys[0] != "ENG" {
		t.Fatalf("DistinctTeamKeys = %v, want [ENG] (ENG's upsert must survive DATA's failure)", keys)
	}
}
