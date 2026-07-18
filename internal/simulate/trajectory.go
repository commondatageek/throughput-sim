package simulate

import "github.com/commondatageek/delivery-forecast/internal/util"

// TrajectoryCell is one (group, percentile) entry in the grouped trajectory
// report: the marginal days to finish that group and the cumulative days from
// the start of the report.
type TrajectoryCell struct {
	MarginalDays   int
	CumulativeDays int
}

// ComputeTrajectoryTable turns per-threshold sorted day-distributions into the
// grouped trajectory report's cells. dists[g] must be the sorted distribution
// of days-to-complete the cumulative threshold sum(groups[:g+1]); all dists
// must have been produced with the same RNG seed so that, within a single
// trial, reaching a larger cumulative count never takes fewer days — this
// guarantees MarginalDays >= 0 and that each percentile's marginal days sum
// exactly to the Total row's days.
//
// The returned slice is indexed [group][percentileIndex]; totals are the
// final group's cumulative days per percentile (equal to the last row).
func ComputeTrajectoryTable(dists [][]int, percentiles []int) (cells [][]TrajectoryCell, totals []int) {
	cells = make([][]TrajectoryCell, len(dists))
	prevCum := make([]int, len(percentiles))
	for g, dist := range dists {
		cells[g] = make([]TrajectoryCell, len(percentiles))
		for pi, p := range percentiles {
			cum := util.PercentileValue(dist, float64(p))
			cells[g][pi] = TrajectoryCell{
				MarginalDays:   cum - prevCum[pi],
				CumulativeDays: cum,
			}
			prevCum[pi] = cum
		}
	}
	totals = append(totals, prevCum...)
	return cells, totals
}
