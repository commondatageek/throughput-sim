package main

import (
	"reflect"
	"testing"
	"time"
)

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
