package simulate

import "testing"

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name         string
		engineersSet bool
		wholeTeam    bool
		team         []string
		want         Mode
		wantErr      bool
	}{
		{"nothing set is an error", false, false, nil, 0, true},
		{"engineers set is anonymous", true, false, nil, ModeAnonymous, false},
		{"whole-team", false, true, nil, ModeFullTeam, false},
		{"named team", false, false, []string{"alice"}, ModeNamedTeam, false},
		{"team wins when only team set", false, false, []string{"alice", "bob"}, ModeNamedTeam, false},
		{"whole-team + engineers conflict", true, true, nil, 0, true},
		{"whole-team + team conflict", false, true, []string{"alice"}, 0, true},
		{"engineers + team conflict", true, false, []string{"alice"}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveMode(c.engineersSet, c.wholeTeam, c.team)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ResolveMode(%v, %v, %v) = %v, want error", c.engineersSet, c.wholeTeam, c.team, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveMode error: %v", err)
			}
			if got != c.want {
				t.Errorf("ResolveMode = %v, want %v", got, c.want)
			}
		})
	}
}

func TestModeLabel(t *testing.T) {
	cases := []struct {
		mode      Mode
		team      []string
		engineers int
		want      string
	}{
		{ModeNamedTeam, []string{"alice", "bob"}, 3, "Team [alice, bob]"},
		{ModeFullTeam, nil, 3, "whole-team throughput"},
		{ModeAnonymous, nil, 3, "3 equivalent engineers"},
	}
	for _, c := range cases {
		if got := ModeLabel(c.mode, c.team, c.engineers); got != c.want {
			t.Errorf("ModeLabel(%v) = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestValidatePool(t *testing.T) {
	named := NewSamplePool(map[string][]int{
		"alice": {1, 0, 2},
		"bob":   {3},
		"empty": {},        // present but every day excluded -> no samples
		"zero":  {0, 0, 0}, // present, has slots, but never completed anything
	})
	full := NewSamplePool(map[string][]int{WholeTeamKey: {0, 1, 0}})
	zeroFull := NewSamplePool(map[string][]int{WholeTeamKey: {0, 0, 0}})
	emptyFull := NewSamplePool(map[string][]int{WholeTeamKey: {}})
	emptyAnon := NewSamplePool(map[string][]int{})
	zeroAnon := NewSamplePool(map[string][]int{"alice": {0, 0}, "bob": {0}})

	cases := []struct {
		name            string
		pool            *SamplePool
		mode            Mode
		team            []string
		requireProgress bool
		wantErr         bool
	}{
		{"named ok", named, ModeNamedTeam, []string{"alice", "bob"}, false, false},
		{"named unknown engineer", named, ModeNamedTeam, []string{"alice", "carol"}, false, true},
		{"named present but no samples", named, ModeNamedTeam, []string{"empty"}, false, true},
		{"full team ok", full, ModeFullTeam, nil, false, false},
		{"full team empty series", emptyFull, ModeFullTeam, nil, false, true},
		{"anonymous ok", named, ModeAnonymous, nil, false, false},
		{"anonymous empty pool", emptyAnon, ModeAnonymous, nil, false, true},

		// requireProgress=false (cmdItems/cmdProbability): all-zero is a fine,
		// legitimate "0 items" answer, not an error.
		{"named all-zero, fixed days ok", named, ModeNamedTeam, []string{"zero"}, false, false},
		{"full team all-zero, fixed days ok", zeroFull, ModeFullTeam, nil, false, false},
		{"anonymous all-zero, fixed days ok", zeroAnon, ModeAnonymous, nil, false, false},

		// requireProgress=true (cmdDays): all-zero means SimulateDaysToComplete*
		// would loop forever (completed never advances), so it must error.
		{"named all-zero, requireProgress errors", named, ModeNamedTeam, []string{"zero"}, true, true},
		{"named mixed zero/nonzero, requireProgress ok", named, ModeNamedTeam, []string{"alice", "zero"}, true, false},
		{"full team all-zero, requireProgress errors", zeroFull, ModeFullTeam, nil, true, true},
		{"full team nonzero, requireProgress ok", full, ModeFullTeam, nil, true, false},
		{"anonymous all-zero, requireProgress errors", zeroAnon, ModeAnonymous, nil, true, true},
		{"anonymous nonzero, requireProgress ok", named, ModeAnonymous, nil, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePool(c.pool, c.mode, c.team, c.requireProgress)
			if c.wantErr && err == nil {
				t.Error("ValidatePool = nil, want error")
			}
			if !c.wantErr && err != nil {
				t.Errorf("ValidatePool = %v, want nil", err)
			}
		})
	}
}
