package simulate

// Params bundles the resolved simulation parameters for the mode-aware
// dispatch functions. Days is used by ItemsInDays; Items by DaysToComplete.
// Progress is optional (nil = no progress reporting). Every field mirrors a
// cmd/forecast flag (and thus a `-config` YAML key of the same name) once
// resolved to its typed, presence-checked value.
type Params struct {
	// Mode is resolved from the `-engineers`/`-team`/`-whole-team` flags via
	// ResolveMode; there is no single `mode` flag or YAML key.
	Mode Mode
	// Team holds the `-team` flag's engineer names (ModeNamedTeam only).
	Team []string
	// Engineers is the `-engineers` flag (ModeAnonymous only).
	Engineers int
	// Days is the `-days` flag (sim items; ignored by DaysToComplete).
	Days int
	// Items is the `-items` flag (sim days/probability; ignored by ItemsInDays).
	Items int
	// Simulations is the `-simulations` flag.
	Simulations int
	// Workers is the `-goroutines` flag.
	Workers int
	// Seed is the `-random-seed` flag, resolved to a concrete value
	// (time-based when the flag was left unset) via resolveSeed.
	Seed int64
	// Progress reports trial completion to RunSimulations; not flag-backed.
	Progress func(done, total int)
}

// ItemsInDays answers "how many items in Days days?" dispatching to the
// appropriate engine based on p.Mode and forwarding p.Progress to RunSimulations.
//
// ModeFullTeam delegates to SimulateItemsInDays with numDailyDraws=1, which
// reproduces the single-draw-per-day loop over the whole-team series exactly.
func ItemsInDays(pool *SamplePool, p Params) []int {
	switch p.Mode {
	case ModeNamedTeam:
		return SimulateItemsInDaysPerEngineer(pool, p.Team, p.Days, p.Simulations, p.Workers, p.Seed, p.Progress)
	case ModeFullTeam:
		return SimulateItemsInDays(pool.PerEngineer[WholeTeamKey], 1, p.Days, p.Simulations, p.Workers, p.Seed, p.Progress)
	default: // ModeAnonymous
		return SimulateItemsInDays(pool.Combined, p.Engineers, p.Days, p.Simulations, p.Workers, p.Seed, p.Progress)
	}
}

// DaysToComplete answers "how many days to finish Items items?" dispatching to
// the appropriate engine based on p.Mode and forwarding p.Progress to RunSimulations.
//
// ModeFullTeam delegates to SimulateDaysToComplete with numEngineers=1, which
// reproduces the single-draw-per-day loop over the whole-team series exactly.
func DaysToComplete(pool *SamplePool, p Params) []int {
	switch p.Mode {
	case ModeNamedTeam:
		return SimulateDaysToCompletePerEngineer(pool, p.Team, p.Items, p.Simulations, p.Workers, p.Seed, p.Progress)
	case ModeFullTeam:
		return SimulateDaysToComplete(pool.PerEngineer[WholeTeamKey], 1, p.Items, p.Simulations, p.Workers, p.Seed, p.Progress)
	default: // ModeAnonymous
		return SimulateDaysToComplete(pool.Combined, p.Engineers, p.Items, p.Simulations, p.Workers, p.Seed, p.Progress)
	}
}
