package simulate

import (
	"fmt"
	"strings"
)

// Mode is which of the three mutually-exclusive sampling strategies a simulation uses.
type Mode int

const (
	ModeAnonymous Mode = iota // pooled anonymous engineers
	ModeFullTeam              // summed whole-team series
	ModeNamedTeam             // individually-modeled named engineers
)

// ResolveMode enforces that exactly one of -engineers, -whole-team, and -team
// is given and reports the selected mode. engineersSet must report whether
// -engineers was explicitly passed (its default value is otherwise
// indistinguishable from an unset flag). There is no implicit default mode:
// an anonymous-engineers run silently assuming some fixed team size would
// produce a plausible-looking but potentially wrong forecast, so the caller
// must state one of the three explicitly.
func ResolveMode(engineersSet, wholeTeam bool, team []string) (Mode, error) {
	if wholeTeam && engineersSet {
		return 0, fmt.Errorf("-whole-team and -engineers are mutually exclusive")
	}
	if wholeTeam && len(team) > 0 {
		return 0, fmt.Errorf("-whole-team and -team are mutually exclusive")
	}
	if engineersSet && len(team) > 0 {
		return 0, fmt.Errorf("-engineers and -team are mutually exclusive")
	}
	switch {
	case len(team) > 0:
		return ModeNamedTeam, nil
	case wholeTeam:
		return ModeFullTeam, nil
	case engineersSet:
		return ModeAnonymous, nil
	default:
		return 0, fmt.Errorf("one of -engineers, -team, or -whole-team must be specified")
	}
}

// ModeLabel returns the noun phrase describing the run,
// e.g. "Team [alice, bob]", "whole-team throughput", or "3 equivalent engineers".
func ModeLabel(mode Mode, team []string, engineers int) string {
	switch mode {
	case ModeNamedTeam:
		return fmt.Sprintf("Team [%s]", strings.Join(team, ", "))
	case ModeFullTeam:
		return "whole-team throughput"
	default:
		return fmt.Sprintf("%d equivalent engineers", engineers)
	}
}

// ValidatePool ensures the chosen mode has samples to draw from before any
// simulation runs, turning what would otherwise be a rng.Intn(0) panic into a
// clear, actionable error. Named engineers must be present AND have at least
// one non-excluded day; anonymous and whole-team modes need a non-empty series.
//
// requireProgress must be true for callers whose simulation loop runs until a
// target item count is reached (SimulateDaysToComplete and its per-engineer
// variant) rather than for a fixed number of days. For those, a pool that sums
// to zero is a guaranteed infinite loop, so it's rejected outright.
// Fixed-day callers pass false: an all-zero pool is a legitimate "0 items"
// / "0% probability" answer, not an error.
func ValidatePool(pool *SamplePool, mode Mode, team []string, requireProgress bool) error {
	switch mode {
	case ModeNamedTeam:
		teamTotal := 0
		for _, name := range team {
			samples, ok := pool.PerEngineer[name]
			if !ok {
				return fmt.Errorf("engineer %q not found in data", name)
			}
			if len(samples) == 0 {
				return fmt.Errorf("engineer %q has no sample days in the selected window (every day excluded?)", name)
			}
			teamTotal += Sum(samples)
		}
		if requireProgress && teamTotal == 0 {
			return fmt.Errorf("team [%s] completed 0 items in the selected window; days-to-complete is undefined (they would never finish)", strings.Join(team, ", "))
		}
	case ModeFullTeam:
		samples := pool.PerEngineer[WholeTeamKey]
		if len(samples) == 0 {
			return fmt.Errorf("no sample days in the selected window (try a different -sample-start/-sample-end)")
		}
		if requireProgress && Sum(samples) == 0 {
			return fmt.Errorf("whole-team throughput was 0 in the selected window; days-to-complete is undefined (it would never finish)")
		}
	default: // ModeAnonymous
		samples := pool.Combined
		if len(samples) == 0 {
			return fmt.Errorf("no completed items in the selected window (try a different -sample-start/-sample-end)")
		}
		if requireProgress && Sum(samples) == 0 {
			return fmt.Errorf("0 items completed in the selected window; days-to-complete is undefined (it would never finish)")
		}
	}
	return nil
}
