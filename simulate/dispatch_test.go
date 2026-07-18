package simulate

import "testing"

// constantPool builds a pool where every engineer's samples are the single
// constant value v, so every simulation trial is fully determined.
func constantPool(v int, engineers ...string) *SamplePool {
	perEngineer := make(map[string][]int)
	for _, eng := range engineers {
		perEngineer[eng] = []int{v}
	}
	return NewSamplePool(perEngineer)
}

func TestItemsInDays_Dispatch(t *testing.T) {
	cases := []struct {
		name string
		pool *SamplePool
		p    Params
		want int
	}{
		{
			name: "named team",
			pool: constantPool(2, "alice", "bob"),
			p:    Params{Mode: ModeNamedTeam, Team: []string{"alice", "bob"}, Days: 10, Simulations: 100, Workers: 4, Seed: 42},
			want: 40, // (2+2) per day * 10 days
		},
		{
			name: "whole team",
			pool: &SamplePool{PerEngineer: map[string][]int{WholeTeamKey: {5}}},
			p:    Params{Mode: ModeFullTeam, Days: 10, Simulations: 100, Workers: 4, Seed: 42},
			want: 50, // 5 per day * 10 days
		},
		{
			name: "anonymous",
			pool: constantPool(3, "alice", "bob"),
			p:    Params{Mode: ModeAnonymous, Engineers: 4, Days: 10, Simulations: 100, Workers: 4, Seed: 42},
			want: 120, // 4 engineers * 10 days * 3 per draw
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ItemsInDays(c.pool, c.p)
			assertAll(t, got, c.want)
		})
	}
}

func TestDaysToComplete_Dispatch(t *testing.T) {
	cases := []struct {
		name string
		pool *SamplePool
		p    Params
		want int
	}{
		{
			name: "named team",
			pool: constantPool(2, "alice", "bob"),
			p:    Params{Mode: ModeNamedTeam, Team: []string{"alice", "bob"}, Items: 20, Simulations: 100, Workers: 4, Seed: 42},
			want: 5, // 4 items/day, need 20 -> 5 days
		},
		{
			name: "whole team",
			pool: &SamplePool{PerEngineer: map[string][]int{WholeTeamKey: {5}}},
			p:    Params{Mode: ModeFullTeam, Items: 20, Simulations: 100, Workers: 4, Seed: 42},
			want: 4, // 5 items/day, need 20 -> 4 days
		},
		{
			name: "anonymous",
			pool: constantPool(3, "alice", "bob"),
			p:    Params{Mode: ModeAnonymous, Engineers: 2, Items: 12, Simulations: 100, Workers: 4, Seed: 42},
			want: 2, // 2 engineers * 3 per draw = 6/day, need 12 -> 2 days
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DaysToComplete(c.pool, c.p)
			assertAll(t, got, c.want)
		})
	}
}
