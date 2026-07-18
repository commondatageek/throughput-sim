package simulate

import "testing"

// TestComputeTrajectoryTable_TelescopesToTotal verifies the core correctness
// property of the grouped trajectory report: because every threshold's
// distribution comes from the same RNG seed, a trial that reaches a larger
// cumulative count never takes fewer days, so marginal days per group are
// never negative and sum exactly to the Total row at every percentile.
func TestComputeTrajectoryTable_TelescopesToTotal(t *testing.T) {
	percentiles := []int{5, 50, 95}
	// Cumulative thresholds 13, 25, 34, 39, 41 (groups 13,12,9,5,2), days
	// distributions are monotonically non-decreasing in threshold for any
	// fixed-seed trial, as DaysToComplete guarantees.
	dists := [][]int{
		{4, 5, 6, 7, 8, 9, 10}, // threshold 13
		{4, 5, 8, 9, 12, 13, 14},
		{6, 7, 10, 10, 16, 17, 18},
		{8, 9, 12, 13, 18, 19, 20},
		{9, 10, 13, 14, 19, 20, 21}, // threshold 41
	}

	cells, totals := ComputeTrajectoryTable(dists, percentiles)

	if len(cells) != len(dists) {
		t.Fatalf("len(cells) = %d, want %d", len(cells), len(dists))
	}
	for pi := range percentiles {
		s := 0
		for g := range dists {
			cell := cells[g][pi]
			if cell.MarginalDays < 0 {
				t.Errorf("percentile %d, group %d: MarginalDays = %d, want >= 0", percentiles[pi], g, cell.MarginalDays)
			}
			s += cell.MarginalDays
		}
		if s != totals[pi] {
			t.Errorf("percentile %d: sum of marginal days = %d, want total %d", percentiles[pi], s, totals[pi])
		}
		lastCum := cells[len(dists)-1][pi].CumulativeDays
		if lastCum != totals[pi] {
			t.Errorf("percentile %d: last group's CumulativeDays = %d, want total %d", percentiles[pi], lastCum, totals[pi])
		}
	}
}
