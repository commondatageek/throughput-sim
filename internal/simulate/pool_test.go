package simulate

import (
	"reflect"
	"testing"
	"time"
)

// day builds a local-midnight calendar date. Fixtures use time.Local (not UTC)
// so they share the same zone as the day-bucketing under test, keeping the
// expected-value assertions correct regardless of the machine's timezone.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// at returns a Completion for engineer eng on the given calendar day, with a
// deliberate mid-day time to confirm time-of-day is truncated away.
func at(eng string, y int, m time.Month, d int) Completion {
	return Completion{Engineer: eng, CompletedAt: time.Date(y, m, d, 14, 30, 0, 0, time.Local)}
}

func TestFilterInvalid_SkipsMalformed(t *testing.T) {
	completedAt := day(2025, 1, 5)
	records := []Completion{
		{Engineer: "alice", CompletedAt: completedAt},
		{Engineer: "", CompletedAt: completedAt},    // no assignee
		{Engineer: "bob", CompletedAt: time.Time{}}, // no completion instant
		{Engineer: "", CompletedAt: time.Time{}},    // both missing
		{Engineer: "carol", CompletedAt: completedAt},
	}

	got, skipped := FilterInvalid(records)

	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}
	want := []Completion{
		{Engineer: "alice", CompletedAt: completedAt},
		{Engineer: "carol", CompletedAt: completedAt},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %+v, want %+v", got, want)
	}
}

func TestFilterInvalid_AllValid(t *testing.T) {
	records := []Completion{
		{Engineer: "alice", CompletedAt: day(2025, 1, 5)},
		{Engineer: "bob", CompletedAt: day(2025, 1, 6)},
	}
	got, skipped := FilterInvalid(records)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(got) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(got))
	}
}

func TestBuildPool_PreservesZeroDays(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11) // 10-day window
	records := []Completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 6), // idx 5
	}
	pool := BuildPool(records, Exclusions{}, start, end, false)

	got := pool.PerEngineer["alice"]
	want := []int{2, 0, 0, 0, 0, 1, 0, 0, 0, 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alice samples = %v, want %v (length must be 10, zeros preserved)", got, want)
	}
}

func TestBuildPool_PartialNowEndDayCountsToday(t *testing.T) {
	// When -sample-end is omitted, resolveEndDate returns a partial "now"
	// (mid-afternoon on 2025-01-05), so DaysBetween grants today an inclusive
	// slot at idx == totalDays-1. A completion landing on that final partial day
	// must be counted, not silently dropped as out-of-range.
	start := day(2025, 1, 1)
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.Local) // partial last day
	records := []Completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 5), // idx 4 == totalDays-1, the partial "today"
		at("alice", 2025, 1, 6), // idx 5, past the window -> dropped
	}
	pool := BuildPool(records, Exclusions{}, start, now, false)

	got := pool.PerEngineer["alice"]
	want := []int{1, 0, 0, 0, 1} // 5 slots; today (idx 4) counted, 1/6 dropped
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alice samples = %v, want %v (today's work must land in the final partial slot)", got, want)
	}
}

func TestBuildPool_DropsOutOfRangeCompletions(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []Completion{
		at("alice", 2024, 12, 25), // before window -> dropped
		at("alice", 2025, 1, 1),   // idx 0
		at("alice", 2025, 1, 15),  // after window -> dropped
	}
	pool := BuildPool(records, Exclusions{}, start, end, false)
	got := pool.PerEngineer["alice"]
	if len(got) != 10 {
		t.Fatalf("len = %d, want 10", len(got))
	}
	s := 0
	for _, v := range got {
		s += v
	}
	if s != 1 {
		t.Fatalf("sum = %d, want 1 (out-of-range completions must be dropped, not counted)", s)
	}
}

func TestBuildPool_GlobalExclusionRemovesSlot(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []Completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 6), // idx 5
	}
	exc := Exclusions{Global: []string{"2025-01-02"}} // removes idx 1
	pool := BuildPool(records, exc, start, end, false)
	got := pool.PerEngineer["alice"]
	want := []int{2, 0, 0, 0, 1, 0, 0, 0, 0} // length 9, idx1 dropped, the 1 shifts left
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildPool_PerEngineerExclusion(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []Completion{
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 1), // idx 0
		at("alice", 2025, 1, 6), // idx 5
	}
	exc := Exclusions{Engineers: map[string][]string{"alice": {"2025-01-06"}}} // removes idx 5
	pool := BuildPool(records, exc, start, end, false)
	got := pool.PerEngineer["alice"]
	want := []int{2, 0, 0, 0, 0, 0, 0, 0, 0} // length 9, the lone "1" removed
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildPool_EngineerSetFromInWindowCompletions(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []Completion{
		at("alice", 2025, 1, 2), // in window
		at("bob", 2024, 12, 20), // bob's only completion is BEFORE the window
	}
	pool := BuildPool(records, Exclusions{}, start, end, false)

	if _, ok := pool.PerEngineer["alice"]; !ok {
		t.Fatal("alice should be in the pool (has an in-window completion)")
	}
	if _, ok := pool.PerEngineer["bob"]; ok {
		t.Fatal("bob must NOT be in the pool: an engineer with no in-window " +
			"completion should not appear")
	}
	if len(pool.PerEngineer) != 1 {
		t.Fatalf("pool should contain exactly 1 engineer, got %d", len(pool.PerEngineer))
	}
}

func TestBuildPool_WholeTeamSumsAndIgnoresPerEngineerExclusions(t *testing.T) {
	start, end := day(2025, 1, 1), day(2025, 1, 11)
	records := []Completion{
		at("alice", 2025, 1, 1), // idx 0
		at("bob", 2025, 1, 1),   // idx 0
		at("bob", 2025, 1, 6),   // idx 5
	}
	// Per-engineer exclusions must be ignored in whole-team mode.
	exc := Exclusions{Engineers: map[string][]string{"bob": {"2025-01-06"}}}
	pool := BuildPool(records, exc, start, end, true)

	if len(pool.PerEngineer) != 1 {
		t.Fatalf("whole-team pool should have exactly one series, got %d", len(pool.PerEngineer))
	}
	got := pool.PerEngineer[WholeTeamKey]
	want := []int{2, 0, 0, 0, 0, 1, 0, 0, 0, 0} // idx0: 1+1, idx5: 1
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGetCombinedSamples_DeterministicOrder(t *testing.T) {
	pool := &SamplePool{PerEngineer: map[string][]int{
		"carol": {5, 6},
		"alice": {1, 2},
		"bob":   {3, 4},
	}}
	want := []int{1, 2, 3, 4, 5, 6} // alice, bob, carol: sorted by engineer name
	for i := range 10 {
		if got := pool.GetCombinedSamples(); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: GetCombinedSamples = %v, want %v", i, got, want)
		}
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
			day(2025, 1, 1), time.Date(2025, 1, 5, 12, 0, 0, 0, time.Local), 5},
		{"midnight end day excluded", day(2025, 1, 1), day(2025, 1, 5), 4},
	}
	for _, c := range cases {
		if got := DaysBetween(c.start, c.end); got != c.want {
			t.Errorf("%s: DaysBetween = %d, want %d", c.name, got, c.want)
		}
	}
}
