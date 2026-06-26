package util

import (
	"math"
	"sort"
)

// ComputePercentile returns what percentile v falls at in a slice sorted in
// ascending order. The result is the cumulative percentile rank: the percentage
// of values in sorted that are less than or equal to v, rounded to the nearest
// integer (0–100). An empty slice returns 0.
func ComputePercentile(sorted []float64, v float64) int {
	if len(sorted) == 0 {
		return 0
	}
	rank := sort.Search(len(sorted), func(i int) bool { return sorted[i] > v })
	return int(math.Round(float64(rank) / float64(len(sorted)) * 100))
}
