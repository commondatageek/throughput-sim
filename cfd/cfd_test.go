package cfd

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t.UTC()
}

func TestNormalizeClamping(t *testing.T) {
	// started_at before created_at must be clamped to created_at.
	r := Issue{
		CreatedAt: day("2024-01-10"),
		StartedAt: day("2024-01-05"), // before created
	}
	ni, ok := Normalize(r)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !ni.LeftBacklog.Equal(ni.Arrival) {
		t.Errorf("LeftBacklog %v should equal Arrival %v", ni.LeftBacklog, ni.Arrival)
	}

	// completed_at before started_at must be clamped to started_at.
	r2 := Issue{
		CreatedAt:   day("2024-01-01"),
		StartedAt:   day("2024-01-10"),
		CompletedAt: day("2024-01-05"), // before started
	}
	ni2, ok := Normalize(r2)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ni2.Exit.Before(ni2.LeftBacklog) {
		t.Errorf("Exit %v should not be before LeftBacklog %v", ni2.Exit, ni2.LeftBacklog)
	}
}

func TestNormalizeNilCreatedAt(t *testing.T) {
	r := Issue{} // zero CreatedAt
	_, ok := Normalize(r)
	if ok {
		t.Fatal("expected ok=false for zero created_at")
	}
}

func TestBuildGridAndInvariants(t *testing.T) {
	start := day("2024-01-01")
	end := day("2024-01-07")

	issues := []NormalizedIssue{
		{Arrival: day("2024-01-01"), LeftBacklog: day("2024-01-03"), Exit: day("2024-01-05"), ExitType: "completed"},
		{Arrival: day("2024-01-02")},
		{Arrival: day("2024-01-04"), LeftBacklog: day("2024-01-04"), Exit: day("2024-01-06"), ExitType: "canceled"},
	}

	rows := BuildGrid(issues, start, end)
	if len(rows) != 7 {
		t.Fatalf("expected 7 rows, got %d", len(rows))
	}

	// Spot-check day 7 (all events have happened).
	last := rows[len(rows)-1]
	if last.Created != 3 {
		t.Errorf("Created on last day: got %d, want 3", last.Created)
	}
	if last.Completed != 1 {
		t.Errorf("Completed on last day: got %d, want 1", last.Completed)
	}

	if err := AssertInvariants(rows); err != nil {
		t.Errorf("invariant violation: %v", err)
	}
}

func TestLinearSlope(t *testing.T) {
	tests := []struct {
		ys   []float64
		want float64
	}{
		{[]float64{0, 1, 2, 3}, 1.0},
		{[]float64{0, 2, 4, 6}, 2.0},
		{[]float64{5, 5, 5, 5}, 0.0},
		{[]float64{3}, 0.0}, // single point → 0
		{[]float64{}, 0.0},
	}
	for _, tc := range tests {
		got := linearSlope(tc.ys)
		if abs := got - tc.want; abs < -1e-9 || abs > 1e-9 {
			t.Errorf("linearSlope(%v) = %v, want %v", tc.ys, got, tc.want)
		}
	}
}
