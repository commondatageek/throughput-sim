package simulate

import "testing"

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
	got := SimulateItemsInDays([]int{2}, 3, 10, 1000, 4, 42, nil)
	assertAll(t, got, 60) // 3 engineers * 10 days * 2 per draw
}

func TestSimulateDaysToComplete_ConstantPool(t *testing.T) {
	got := SimulateDaysToComplete([]int{2}, 1, 10, 1000, 4, 42, nil)
	assertAll(t, got, 5) // 2 items/day, need 10 -> 5 days

	// Inexact case guards the termination off-by-one: ceil(11/2) = 6.
	got = SimulateDaysToComplete([]int{2}, 1, 11, 1000, 4, 42, nil)
	assertAll(t, got, 6)
}

func TestSimulateItemsInDaysPerEngineer_ConstantPool(t *testing.T) {
	pool := &SamplePool{PerEngineer: map[string][]int{
		"alice": {2},
		"bob":   {3},
	}}
	got := SimulateItemsInDaysPerEngineer(pool, []string{"alice", "bob"}, 10, 1000, 4, 42, nil)
	assertAll(t, got, 50) // (2+3) per day * 10 days
}

func TestSimulateDaysToCompletePerEngineer_ConstantPool(t *testing.T) {
	pool := &SamplePool{PerEngineer: map[string][]int{
		"alice": {2},
		"bob":   {3},
	}}
	got := SimulateDaysToCompletePerEngineer(pool, []string{"alice", "bob"}, 10, 1000, 4, 42, nil)
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
		got := ProbabilityAtLeast(dist, c.n)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("ProbabilityAtLeast(dist, %d) = %v, want %v", c.n, got, c.want)
		}
	}
	if got := ProbabilityAtLeast(nil, 1); got != 0 {
		t.Errorf("ProbabilityAtLeast(empty) = %v, want 0", got)
	}
}
