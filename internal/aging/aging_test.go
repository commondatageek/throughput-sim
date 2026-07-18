package aging

import (
	"sort"
	"testing"
	"time"

	"github.com/commondatageek/delivery-forecast/internal/linear"
)

func mustTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t.UTC()
}

func TestCycleTimesFiltering(t *testing.T) {
	issues := []linear.Issue{
		{StartedAt: mustTime("2024-01-01"), CompletedAt: mustTime("2024-01-11")}, // 10 days
		{StartedAt: mustTime("2024-01-01"), CompletedAt: mustTime("2024-01-02")}, // 1 day
		{StartedAt: mustTime("2024-01-01"), CompletedAt: mustTime("2024-01-06")}, // 5 days
		{CompletedAt: mustTime("2024-01-10")},                                    // no StartedAt → skip
		{StartedAt: mustTime("2024-01-01")},                                      // no CompletedAt → skip
	}

	// No min cycle time: all three valid issues pass.
	got := CycleTimes(issues, 0)
	if len(got) != 3 {
		t.Errorf("no min: got %d cycle times, want 3", len(got))
	}

	// Min 3 days: 1-day issue filtered out.
	got = CycleTimes(issues, 3*24*time.Hour)
	if len(got) != 2 {
		t.Errorf("min 3d: got %d cycle times, want 2", len(got))
	}

	// Min 6 days: only the 10-day issue passes.
	got = CycleTimes(issues, 6*24*time.Hour)
	if len(got) != 1 {
		t.Fatalf("min 6d: got %d cycle times, want 1", len(got))
	}
	if got[0] != 10.0 {
		t.Errorf("expected 10.0 days, got %v", got[0])
	}
}

func TestInProgressItemsAgeDays(t *testing.T) {
	today := mustTime("2024-03-01")
	issues := []linear.Issue{
		{Identifier: "ENG-1", StartedAt: mustTime("2024-02-20")}, // 10 days ago
		{Identifier: "ENG-2", StartedAt: mustTime("2024-02-29")}, // 1 day ago
		{Identifier: "ENG-3"},                                     // no StartedAt → skip
	}

	items := InProgressItems(issues, today)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].AgeDays != 10.0 {
		t.Errorf("ENG-1 AgeDays: got %v, want 10.0", items[0].AgeDays)
	}
	if items[1].AgeDays != 1.0 {
		t.Errorf("ENG-2 AgeDays: got %v, want 1.0", items[1].AgeDays)
	}
}

func TestCompletedItemsFiltering(t *testing.T) {
	issues := []linear.Issue{
		{Identifier: "ENG-1", StartedAt: mustTime("2024-01-01"), CompletedAt: mustTime("2024-01-11")}, // 10 days
		{Identifier: "ENG-2", StartedAt: mustTime("2024-01-01"), CompletedAt: mustTime("2024-01-02")}, // 1 day
		{Identifier: "ENG-3", CompletedAt: mustTime("2024-01-10")},                                    // no StartedAt → skip
		{Identifier: "ENG-4", StartedAt: mustTime("2024-01-01")},                                      // no CompletedAt → skip
	}

	items := CompletedItems(issues, 0)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Identifier != "ENG-1" || items[0].AgeDays != 10.0 {
		t.Errorf("ENG-1: got %+v", items[0])
	}
	if items[1].Identifier != "ENG-2" || items[1].AgeDays != 1.0 {
		t.Errorf("ENG-2: got %+v", items[1])
	}

	// Min 3 days: 1-day issue filtered out.
	items = CompletedItems(issues, 3*24*time.Hour)
	if len(items) != 1 {
		t.Fatalf("min 3d: got %d items, want 1", len(items))
	}
	if items[0].Identifier != "ENG-1" {
		t.Errorf("min 3d: got %+v, want ENG-1", items[0])
	}
}

func TestRankItems(t *testing.T) {
	cycleTimes := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	sort.Float64s(cycleTimes)

	items := []Item{
		{AgeDays: 5.0},  // 50th percentile
		{AgeDays: 10.0}, // 100th percentile
		{AgeDays: 0.5},  // 0th percentile (below all)
	}

	RankItems(items, cycleTimes)

	if items[0].Percentile != 50 {
		t.Errorf("5.0 days: got %d%%, want 50%%", items[0].Percentile)
	}
	if items[1].Percentile != 100 {
		t.Errorf("10.0 days: got %d%%, want 100%%", items[1].Percentile)
	}
	if items[2].Percentile != 0 {
		t.Errorf("0.5 days: got %d%%, want 0%%", items[2].Percentile)
	}
}
