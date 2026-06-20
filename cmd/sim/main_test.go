package main

import (
	"flag"
	"reflect"
	"testing"
	"time"

	"forecasting/internal/linear"
)

func TestValidCompletions_SkipsMalformed(t *testing.T) {
	completedAt := day(2025, 1, 5)
	issues := []linear.Issue{
		{Identifier: "ENG-1", Assignee: "alice", CompletedAt: completedAt},
		{Identifier: "ENG-2", Assignee: "", CompletedAt: completedAt},    // no assignee
		{Identifier: "ENG-3", Assignee: "bob", CompletedAt: time.Time{}}, // no completion instant
		{Identifier: "ENG-4", Assignee: "", CompletedAt: time.Time{}},    // both missing
		{Identifier: "ENG-5", Assignee: "carol", CompletedAt: completedAt},
	}

	records, skipped := validCompletions(issues)

	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}
	want := []completion{
		{Engineer: "alice", CompletedAt: completedAt},
		{Engineer: "carol", CompletedAt: completedAt},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("records = %+v, want %+v", records, want)
	}
}

func TestValidCompletions_AllValid(t *testing.T) {
	issues := []linear.Issue{
		{Identifier: "ENG-1", Assignee: "alice", CompletedAt: day(2025, 1, 5)},
		{Identifier: "ENG-2", Assignee: "bob", CompletedAt: day(2025, 1, 6)},
	}
	records, skipped := validCompletions(issues)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// at returns a completion for engineer eng on the given calendar day, with a
// deliberate mid-day time to confirm time-of-day is truncated away.
func at(eng string, y int, m time.Month, d int) completion {
	return completion{Engineer: eng, CompletedAt: time.Date(y, m, d, 14, 30, 0, 0, time.UTC)}
}

func TestBuildPool_PreservesZeroDays(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11) // 10-day window
	records := []completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 6), // idx 5
	}
	pool := buildPool(records, Exclusions{}, start, end, false)

	got := pool.PerEngineer["alice"]
	want := []int{2, 0, 0, 0, 0, 1, 0, 0, 0, 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alice samples = %v, want %v (length must be 10, zeros preserved)", got, want)
	}
}

func TestBuildPool_PartialNowEndDayCountsToday(t *testing.T) {
	// When -sample-end is omitted, resolveEndDate returns a partial "now"
	// (mid-afternoon on 2025-01-05), so daysBetween grants today an inclusive
	// slot at idx == totalDays-1. A completion landing on that final partial day
	// must be counted, not silently dropped as out-of-range. This is the seam
	// between daysBetween's +1 partial-day branch and buildPool's idx bounds
	// check, the off-by-one a refactor of either would most easily reintroduce.
	start := day(2025, 1, 1)
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.UTC) // partial last day
	records := []completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 5), // idx 4 == totalDays-1, the partial "today"
		at("alice", 2025, 1, 6), // idx 5, past the window -> dropped
	}
	pool := buildPool(records, Exclusions{}, start, now, false)

	got := pool.PerEngineer["alice"]
	want := []int{1, 0, 0, 0, 1} // 5 slots; today (idx 4) counted, 1/6 dropped
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alice samples = %v, want %v (today's work must land in the final partial slot)", got, want)
	}
}

func TestBuildPool_DropsOutOfRangeCompletions(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []completion{
		at("alice", 2024, 12, 25), // before window -> dropped
		at("alice", 2025, 1, 1),   // idx 0
		at("alice", 2025, 1, 15),  // after window -> dropped
	}
	pool := buildPool(records, Exclusions{}, start, end, false)
	got := pool.PerEngineer["alice"]
	if len(got) != 10 {
		t.Fatalf("len = %d, want 10", len(got))
	}
	sum := 0
	for _, v := range got {
		sum += v
	}
	if sum != 1 {
		t.Fatalf("sum = %d, want 1 (out-of-range completions must be dropped, not counted)", sum)
	}
}

func TestBuildPool_GlobalExclusionRemovesSlot(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 6), // idx 5
	}
	exc := Exclusions{Global: []string{"2025-01-02"}} // removes idx 1
	pool := buildPool(records, exc, start, end, false)
	got := pool.PerEngineer["alice"]
	want := []int{2, 0, 0, 0, 1, 0, 0, 0, 0} // length 9, idx1 dropped, the 1 shifts left
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildPool_PerEngineerExclusion(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 6), // idx 5
	}
	exc := Exclusions{Engineers: map[string][]string{"alice": {"2025-01-06"}}} // removes idx 5
	pool := buildPool(records, exc, start, end, false)
	got := pool.PerEngineer["alice"]
	want := []int{2, 0, 0, 0, 0, 0, 0, 0, 0} // length 9, the lone "1" removed
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildPool_EngineerSetFromInWindowCompletions(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []completion{
		at("alice", 2025, 1, 2), // in window
		at("bob", 2024, 12, 20), // bob's only completion is BEFORE the window
	}
	pool := buildPool(records, Exclusions{}, start, end, false)

	if _, ok := pool.PerEngineer["alice"]; !ok {
		t.Fatal("alice should be in the pool (has an in-window completion)")
	}
	if _, ok := pool.PerEngineer["bob"]; ok {
		t.Fatal("bob must NOT be in the pool: an engineer with no in-window " +
			"completion should not appear (unified DB/NDJSON behavior)")
	}
	if len(pool.PerEngineer) != 1 {
		t.Fatalf("pool should contain exactly 1 engineer, got %d", len(pool.PerEngineer))
	}
}

func TestBuildPool_WholeTeamSumsAndIgnoresPerEngineerExclusions(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []completion{
		at("alice", 2025, 1, 1), // idx 0
		at("bob", 2025, 1, 1),   // idx 0
		at("bob", 2025, 1, 6),   // idx 5
	}
	// Per-engineer exclusions must be ignored in whole-team mode.
	exc := Exclusions{Engineers: map[string][]string{"bob": {"2025-01-06"}}}
	pool := buildPool(records, exc, start, end, true)

	if len(pool.PerEngineer) != 1 {
		t.Fatalf("whole-team pool should have exactly one series, got %d", len(pool.PerEngineer))
	}
	got := pool.PerEngineer["__whole_team__"]
	want := []int{2, 0, 0, 0, 0, 1, 0, 0, 0, 0} // idx0: 1+1, idx5: 1
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDaysBetween(t *testing.T) {
	cases := []struct {
		name       string
		start, end time.Time
		want       int
	}{
		{"whole days, midnight end excludes that day", day(2025, 1, 1), day(2025, 1, 11), 10},
		{"partial end day gets one inclusive slot",
			day(2025, 1, 1), time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC), 5},
		{"midnight end day excluded", day(2025, 1, 1), day(2025, 1, 5), 4},
	}
	for _, c := range cases {
		if got := daysBetween(c.start, c.end); got != c.want {
			t.Errorf("%s: daysBetween = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestPercentile(t *testing.T) {
	sorted := make([]int, 100) // 0..99
	for i := range sorted {
		sorted[i] = i
	}
	cases := []struct {
		p    float64
		want int
	}{
		{0, 0},
		{50, 50},
		{100, 99},
	}
	for _, c := range cases {
		if got := Percentile(sorted, c.p); got != c.want {
			t.Errorf("Percentile(p=%v) = %d, want %d", c.p, got, c.want)
		}
	}
	if got := Percentile(nil, 50); got != 0 {
		t.Errorf("Percentile(empty) = %d, want 0", got)
	}
}

// assertAll fails unless every element of got equals want. Used with constant
// sample pools, where each simulation trial is fully determined and must be identical.
func assertAll(t *testing.T, got []int, want int) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("got empty result slice, want %d non-empty", want)
	}
	for i, v := range got {
		if v != want {
			t.Fatalf("result[%d] = %d, want every element == %d", i, v, want)
		}
	}
}

func TestSimulateItemsInDays_ConstantPool(t *testing.T) {
	got := SimulateItemsInDays([]int{2}, 3, 10, 1000, 4, 42)
	assertAll(t, got, 60) // 3 engineers * 10 days * 2 per draw
}

func TestSimulateDaysToComplete_ConstantPool(t *testing.T) {
	got := SimulateDaysToComplete([]int{2}, 1, 10, 1000, 4, 42)
	assertAll(t, got, 5) // 2 items/day, need 10 -> 5 days

	// Inexact case guards the termination off-by-one: ceil(11/2) = 6.
	got = SimulateDaysToComplete([]int{2}, 1, 11, 1000, 4, 42)
	assertAll(t, got, 6)
}

func TestSimulateItemsInDaysPerEngineer_ConstantPool(t *testing.T) {
	pool := &SamplePool{PerEngineer: map[string][]int{
		"alice": {2},
		"bob":   {3},
	}}
	got := SimulateItemsInDaysPerEngineer(pool, []string{"alice", "bob"}, 10, 1000, 4, 42)
	assertAll(t, got, 50) // (2+3) per day * 10 days
}

func TestSimulateDaysToCompletePerEngineer_ConstantPool(t *testing.T) {
	pool := &SamplePool{PerEngineer: map[string][]int{
		"alice": {2},
		"bob":   {3},
	}}
	got := SimulateDaysToCompletePerEngineer(pool, []string{"alice", "bob"}, 10, 1000, 4, 42)
	assertAll(t, got, 2) // 5/day, need 10 -> 2 days
}

func TestProbabilityAtLeast(t *testing.T) {
	dist := []int{1, 2, 3, 4}
	cases := []struct {
		n    int
		want float64
	}{
		{1, 100.0}, // all 4 are >= 1
		{3, 50.0},  // 3 and 4 -> 2/4
		{4, 25.0},  // only 4 -> 1/4
		{5, 0.0},   // none
	}
	for _, c := range cases {
		got := probabilityAtLeast(dist, c.n)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("probabilityAtLeast(dist, %d) = %v, want %v", c.n, got, c.want)
		}
	}
	if got := probabilityAtLeast(nil, 1); got != 0 {
		t.Errorf("probabilityAtLeast(empty) = %v, want 0", got)
	}
}

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name         string
		engineersSet bool
		wholeTeam    bool
		team         []string
		want         samplingMode
		wantErr      bool
	}{
		{"default is anonymous", false, false, nil, modeAnonymous, false},
		{"engineers set stays anonymous", true, false, nil, modeAnonymous, false},
		{"whole-team", false, true, nil, modeFullTeam, false},
		{"named team", false, false, []string{"alice"}, modeNamedTeam, false},
		{"team wins when only team set", false, false, []string{"alice", "bob"}, modeNamedTeam, false},
		{"whole-team + engineers conflict", true, true, nil, 0, true},
		{"whole-team + team conflict", false, true, []string{"alice"}, 0, true},
		{"engineers + team conflict", true, false, []string{"alice"}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveMode(c.engineersSet, c.wholeTeam, c.team)
			if c.wantErr {
				if err == nil {
					t.Fatalf("resolveMode(%v, %v, %v) = %v, want error", c.engineersSet, c.wholeTeam, c.team, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMode error: %v", err)
			}
			if got != c.want {
				t.Errorf("resolveMode = %v, want %v", got, c.want)
			}
		})
	}
}

func TestModeLabel(t *testing.T) {
	cases := []struct {
		mode      samplingMode
		team      []string
		engineers int
		want      string
	}{
		{modeNamedTeam, []string{"alice", "bob"}, 3, "Team [alice, bob]"},
		{modeFullTeam, nil, 3, "whole-team throughput"},
		{modeAnonymous, nil, 3, "3 equivalent engineers"},
	}
	for _, c := range cases {
		if got := modeLabel(c.mode, c.team, c.engineers); got != c.want {
			t.Errorf("modeLabel(%v) = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestValidatePool(t *testing.T) {
	named := &SamplePool{PerEngineer: map[string][]int{
		"alice": {1, 0, 2},
		"bob":   {3},
		"empty": {},        // present but every day excluded -> no samples
		"zero":  {0, 0, 0}, // present, has slots, but never completed anything
	}}
	full := &SamplePool{PerEngineer: map[string][]int{"__whole_team__": {0, 1, 0}}}
	zeroFull := &SamplePool{PerEngineer: map[string][]int{"__whole_team__": {0, 0, 0}}}
	emptyFull := &SamplePool{PerEngineer: map[string][]int{"__whole_team__": {}}}
	emptyAnon := &SamplePool{PerEngineer: map[string][]int{}}
	zeroAnon := &SamplePool{PerEngineer: map[string][]int{"alice": {0, 0}, "bob": {0}}}

	cases := []struct {
		name            string
		pool            *SamplePool
		mode            samplingMode
		team            []string
		requireProgress bool
		wantErr         bool
	}{
		{"named ok", named, modeNamedTeam, []string{"alice", "bob"}, false, false},
		{"named unknown engineer", named, modeNamedTeam, []string{"alice", "carol"}, false, true},
		{"named present but no samples", named, modeNamedTeam, []string{"empty"}, false, true},
		{"full team ok", full, modeFullTeam, nil, false, false},
		{"full team empty series", emptyFull, modeFullTeam, nil, false, true},
		{"anonymous ok", named, modeAnonymous, nil, false, false},
		{"anonymous empty pool", emptyAnon, modeAnonymous, nil, false, true},

		// requireProgress=false (cmdItems/cmdProbability): all-zero is a fine,
		// legitimate "0 items" answer, not an error.
		{"named all-zero, fixed days ok", named, modeNamedTeam, []string{"zero"}, false, false},
		{"full team all-zero, fixed days ok", zeroFull, modeFullTeam, nil, false, false},
		{"anonymous all-zero, fixed days ok", zeroAnon, modeAnonymous, nil, false, false},

		// requireProgress=true (cmdDays): all-zero means SimulateDaysToComplete*
		// would loop forever (completed never advances), so it must error.
		{"named all-zero, requireProgress errors", named, modeNamedTeam, []string{"zero"}, true, true},
		{"named mixed zero/nonzero, requireProgress ok", named, modeNamedTeam, []string{"alice", "zero"}, true, false},
		{"full team all-zero, requireProgress errors", zeroFull, modeFullTeam, nil, true, true},
		{"full team nonzero, requireProgress ok", full, modeFullTeam, nil, true, false},
		{"anonymous all-zero, requireProgress errors", zeroAnon, modeAnonymous, nil, true, true},
		{"anonymous nonzero, requireProgress ok", named, modeAnonymous, nil, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePool(c.pool, c.mode, c.team, c.requireProgress)
			if c.wantErr && err == nil {
				t.Error("validatePool = nil, want error")
			}
			if !c.wantErr && err != nil {
				t.Errorf("validatePool = %v, want nil", err)
			}
		})
	}
}

// newSampleEndFlags returns a FlagSet defining the flags resolveEndDate and
// resolveSeed inspect. The flags are only registered as "set" via fs.Set, which
// is exactly what isFlagSet (backed by fs.Visit) keys off of.
func newSampleEndFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("sample-end", "", "")
	fs.Int64("random-seed", 0, "")
	return fs
}

func TestResolveEndDate(t *testing.T) {
	start := day(2025, 1, 1)
	// Mid-afternoon "now": the unset branch must return this verbatim so that
	// daysBetween counts today as a partial, inclusive slot (the +1 branch).
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.UTC)

	// Unset -sample-end: defaults to now, today included as a partial slot.
	fs := newSampleEndFlags()
	end, err := resolveEndDate(fs, "", now)
	if err != nil {
		t.Fatalf("resolveEndDate (unset) error: %v", err)
	}
	if !end.Equal(now) {
		t.Errorf("resolveEndDate (unset) = %v, want now %v", end, now)
	}
	if got := daysBetween(start, end); got != 5 {
		t.Errorf("daysBetween with default now: got %d, want 5 (today is a partial slot)", got)
	}

	// Explicit -sample-end: parsed as a midnight calendar date, excluding that
	// whole day and ignoring now.
	fs = newSampleEndFlags()
	if err := fs.Set("sample-end", "2025-01-05"); err != nil {
		t.Fatal(err)
	}
	end, err = resolveEndDate(fs, "2025-01-05", now)
	if err != nil {
		t.Fatalf("resolveEndDate (set) error: %v", err)
	}
	if !end.Equal(day(2025, 1, 5)) {
		t.Errorf("resolveEndDate (set) = %v, want midnight 2025-01-05", end)
	}
	if got := daysBetween(start, end); got != 4 {
		t.Errorf("daysBetween with explicit midnight end: got %d, want 4", got)
	}
}

func TestResolveSeed(t *testing.T) {
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.UTC)

	// Unset -random-seed: time-based seed derived from now.
	fs := newSampleEndFlags()
	if got := resolveSeed(fs, 99, now); got != now.UnixNano() {
		t.Errorf("resolveSeed (unset) = %d, want %d", got, now.UnixNano())
	}

	// Explicit -random-seed: returned verbatim, now ignored.
	fs = newSampleEndFlags()
	if err := fs.Set("random-seed", "99"); err != nil {
		t.Fatal(err)
	}
	if got := resolveSeed(fs, 99, now); got != 99 {
		t.Errorf("resolveSeed (set) = %d, want 99", got)
	}
}
