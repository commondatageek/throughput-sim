package util

import "testing"

func TestComputePercentile(t *testing.T) {
	tests := []struct {
		name   string
		sorted []float64
		v      float64
		want   int
	}{
		{"empty", nil, 5, 0},
		{"below min", []float64{10, 20, 30, 40}, 5, 0},
		{"at min", []float64{10, 20, 30, 40}, 10, 25},
		{"at max", []float64{10, 20, 30, 40}, 40, 100},
		{"above max", []float64{10, 20, 30, 40}, 100, 100},
		{"middle exact", []float64{10, 20, 30, 40}, 20, 50},
		{"between values", []float64{10, 20, 30, 40}, 25, 50},
		{"single element below", []float64{42}, 0, 0},
		{"single element equal", []float64{42}, 42, 100},
		// Ties count toward the rank: both 20s are <= v, so 3 of 5 = 60.
		{"ties", []float64{10, 20, 20, 30, 40}, 20, 60},
		{"rounding down", []float64{1, 2, 3}, 1, 33},
		{"rounding up", []float64{1, 2, 3}, 2, 67},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputePercentile(tt.sorted, tt.v); got != tt.want {
				t.Errorf("ComputePercentile(%v, %v) = %d, want %d", tt.sorted, tt.v, got, tt.want)
			}
		})
	}
}
