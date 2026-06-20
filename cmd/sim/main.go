package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"

	"github.com/mattn/go-isatty"
)

// --- Exclusions ---

type Exclusions struct {
	Global    []string            `json:"global"`
	Engineers map[string][]string `json:"engineers"`
}

func loadExclusions(path string) (Exclusions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Exclusions{}, nil
		}
		return Exclusions{}, fmt.Errorf("reading exclusions file: %w", err)
	}
	var exc Exclusions
	if err := json.Unmarshal(data, &exc); err != nil {
		return Exclusions{}, fmt.Errorf("parsing exclusions file: %w", err)
	}
	return exc, nil
}

// --- In-memory sample pool ---

type SamplePool struct {
	PerEngineer map[string][]int // engineer name -> their daily completion samples
}

func (p *SamplePool) DrawFromEngineer(engineer string, rng *rand.Rand) int {
	samples := p.PerEngineer[engineer]
	return samples[rng.Intn(len(samples))]
}

func (p *SamplePool) GetCombinedSamples() []int {
	var combined []int
	for _, samples := range p.PerEngineer {
		combined = append(combined, samples...)
	}
	return combined
}

// completion is a normalized completed-issue record: the engineer and the
// instant they completed something. loadPool reduces DB rows to a slice of
// these and hands them to buildPool.
type completion struct {
	Engineer    string
	CompletedAt time.Time
}

// buildPool bins completions into per-engineer daily completion counts over the
// half-open window [startDate, endDate), applies exclusions, and returns the
// resulting SamplePool. It is pure (no file/DB/clock access), so it is the
// single unit-testable home of the bucketing logic both loaders share.
//
// The pool deliberately preserves zero-completion days: each engineer's slice
// has one slot per non-excluded day in the window, so a day with no completions
// contributes a 0 sample. Dropping those would bias every forecast upward.
//
// The engineer set is derived solely from records: an engineer appears in the
// pool only if they have at least one completion inside the window. Completions
// outside the window are ignored entirely (neither counted nor do they create
// an engineer). In whole-team mode all engineers are summed into a single
// "__whole_team__" series.
func buildPool(records []completion, exc Exclusions, startDate, endDate time.Time, wholeTeam bool) *SamplePool {
	totalDays := daysBetween(startDate, endDate)

	// Build the global excluded day-index set.
	globalExcluded := make(map[int]bool)
	for _, ds := range exc.Global {
		t, err := time.ParseInLocation("2006-01-02", ds, time.UTC)
		if err != nil {
			continue
		}
		idx := int(t.Sub(startDate).Hours() / 24)
		globalExcluded[idx] = true
	}

	type engData struct {
		counts []int
	}
	engineers := make(map[string]*engData)
	for _, r := range records {
		t := r.CompletedAt.UTC().Truncate(24 * time.Hour)
		idx := int(t.Sub(startDate).Hours() / 24)
		if idx < 0 || idx >= totalDays {
			continue // out-of-window: don't count, don't create the engineer
		}
		eng, ok := engineers[r.Engineer]
		if !ok {
			eng = &engData{counts: make([]int, totalDays)}
			engineers[r.Engineer] = eng
		}
		eng.counts[idx]++
	}

	pool := &SamplePool{PerEngineer: make(map[string][]int)}

	if wholeTeam {
		// Sum all engineers' completions per day. Only global exclusions apply.
		teamCounts := make([]int, totalDays)
		for _, eng := range engineers {
			for i, count := range eng.counts {
				teamCounts[i] += count
			}
		}
		var teamSamples []int
		for i, count := range teamCounts {
			if !globalExcluded[i] {
				teamSamples = append(teamSamples, count)
			}
		}
		pool.PerEngineer["__whole_team__"] = teamSamples
	} else {
		for name, eng := range engineers {
			// Per-engineer excluded set = global + engineer-specific.
			excluded := make(map[int]bool, len(globalExcluded))
			for k := range globalExcluded {
				excluded[k] = true
			}
			for _, ds := range exc.Engineers[name] {
				t, err := time.ParseInLocation("2006-01-02", ds, time.UTC)
				if err != nil {
					continue
				}
				idx := int(t.Sub(startDate).Hours() / 24)
				excluded[idx] = true
			}
			var engineerSamples []int
			for i, count := range eng.counts {
				if !excluded[i] {
					engineerSamples = append(engineerSamples, count)
				}
			}
			pool.PerEngineer[name] = engineerSamples
		}
	}

	return pool
}

// --- Progress reporting ---

// progressBar renders a simple text progress bar to stderr, updating
// at most ~200 times over the run so it doesn't slow down tight loops.
// It's a no-op when stderr isn't a terminal (e.g. piped output, CI logs).
type progressBar struct {
	enabled bool
	total   int
	step    int
	mu      sync.Mutex
}

func newProgressBar(total int) *progressBar {
	return &progressBar{
		enabled: isatty.IsTerminal(os.Stderr.Fd()) && total > 0,
		total:   total,
		step:    max(1, total/200),
	}
}

func (b *progressBar) update(done int) {
	if !b.enabled || (done != b.total && done%b.step != 0) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	const width = 30
	filled := width * done / b.total
	fmt.Fprintf(os.Stderr, "\r[%s%s] %d/%d", strings.Repeat("=", filled), strings.Repeat(" ", width-filled), done, b.total)
	if done == b.total {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

// --- Simulation ---

// runSimulations runs numSimulations independent trials across numWorkers
// goroutines and returns the sorted distribution of their results. Each worker
// owns a disjoint range of the results slice (so no locking is needed on the
// results) and gets its own *rand.Rand seeded from seed plus its worker index,
// since rand.Rand is not safe for concurrent use.
func runSimulations(numSimulations, numWorkers int, seed int64, trial func(rng *rand.Rand) int) []int {
	if numWorkers < 1 {
		numWorkers = 1
	}
	results := make([]int, numSimulations)
	bar := newProgressBar(numSimulations)
	var done atomic.Int64
	var wg sync.WaitGroup

	chunk := (numSimulations + numWorkers - 1) / numWorkers
	for w := 0; w < numWorkers; w++ {
		start := w * chunk
		if start >= numSimulations {
			break
		}
		end := min(start+chunk, numSimulations)
		wg.Add(1)
		go func(start, end, w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed + int64(w)))
			for i := start; i < end; i++ {
				results[i] = trial(rng)
				bar.update(int(done.Add(1)))
			}
		}(start, end, w)
	}
	wg.Wait()
	sort.Ints(results)
	return results
}

// SimulateItemsInDays runs N simulations and returns the distribution of
// total items completed in `days` days by `numDailyDraws` equivalent engineers.
// samples is a flat slice of daily completion counts to sample from.
func SimulateItemsInDays(samples []int, numDailyDraws, days, numSimulations, numWorkers int, seed int64) []int {
	return runSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		total := 0
		for e := 0; e < numDailyDraws; e++ {
			for d := 0; d < days; d++ {
				total += samples[rng.Intn(len(samples))]
			}
		}
		return total
	})
}

// SimulateItemsInDaysPerEngineer runs N simulations for named engineers,
// where each engineer samples from their own historical performance pool.
func SimulateItemsInDaysPerEngineer(pool *SamplePool, teamMembers []string, days, numSimulations, numWorkers int, seed int64) []int {
	return runSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		total := 0
		for _, engineer := range teamMembers {
			for d := 0; d < days; d++ {
				total += pool.DrawFromEngineer(engineer, rng)
			}
		}
		return total
	})
}

// SimulateDaysToCompletePerEngineer runs N simulations for named engineers,
// where each engineer samples from their own historical performance pool.
func SimulateDaysToCompletePerEngineer(pool *SamplePool, teamMembers []string, items, numSimulations, numWorkers int, seed int64) []int {
	return runSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		completed := 0
		days := 0
		for completed < items {
			days++
			for _, engineer := range teamMembers {
				completed += pool.DrawFromEngineer(engineer, rng)
			}
		}
		return days
	})
}

// SimulateDaysToComplete runs N simulations and returns the distribution of
// days needed for `numEngineers` equivalent engineers to complete `items` items.
// samples is a flat slice of daily completion counts to sample from.
func SimulateDaysToComplete(samples []int, numEngineers, items, numSimulations, numWorkers int, seed int64) []int {
	return runSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		completed := 0
		days := 0
		for completed < items {
			days++
			for e := 0; e < numEngineers; e++ {
				completed += samples[rng.Intn(len(samples))]
			}
		}
		return days
	})
}

// Percentile returns the value at the given percentile (0-100) from a sorted slice.
func Percentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p/100.0*float64(len(sorted)-1) + 0.5)
	return sorted[idx]
}

// probabilityAtLeast returns the percentage (0-100) of simulation results in dist
// that met or exceeded n. Returns 0 for an empty distribution.
func probabilityAtLeast(dist []int, n int) float64 {
	if len(dist) == 0 {
		return 0
	}
	count := 0
	for _, v := range dist {
		if v >= n {
			count++
		}
	}
	return float64(count) / float64(len(dist)) * 100.0
}

// --- Mode selection ---

// samplingMode is which of the three mutually-exclusive sampling strategies a
// command runs under: pooled anonymous engineers, the summed whole team, or a
// set of individually-modeled named engineers.
type samplingMode int

const (
	modeAnonymous samplingMode = iota
	modeFullTeam
	modeNamedTeam
)

// resolveMode enforces that -engineers, -whole-team, and -team are mutually
// exclusive and reports the selected mode. engineersSet must report whether
// -engineers was explicitly passed (its default value is otherwise
// indistinguishable from an unset flag). It is pure, so the branching the three
// commands share lives in one table-testable place.
func resolveMode(engineersSet, wholeTeam bool, team []string) (samplingMode, error) {
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
		return modeNamedTeam, nil
	case wholeTeam:
		return modeFullTeam, nil
	default:
		return modeAnonymous, nil
	}
}

// modeLabel returns the noun phrase each command uses to describe the run,
// e.g. "Team [alice, bob]", "whole-team throughput", or "3 equivalent engineers".
func modeLabel(mode samplingMode, team []string, engineers int) string {
	switch mode {
	case modeNamedTeam:
		return fmt.Sprintf("Team [%s]", strings.Join(team, ", "))
	case modeFullTeam:
		return "whole-team throughput"
	default:
		return fmt.Sprintf("%d equivalent engineers", engineers)
	}
}

// sum returns the total of a slice of ints.
func sum(samples []int) int {
	total := 0
	for _, v := range samples {
		total += v
	}
	return total
}

// validatePool ensures the chosen mode actually has samples to draw from before
// any simulation runs, turning what would otherwise be an rng.Intn(0) panic deep
// in a worker goroutine into a clear, actionable error. Named engineers must be
// present AND have at least one non-excluded day; anonymous and whole-team modes
// need a non-empty daily series.
//
// requireProgress must be true for callers whose simulation loop runs until a
// target item count is reached (SimulateDaysToComplete and its per-engineer
// variant, used by cmdDays) rather than for a fixed number of days. For those,
// a pool that sums to zero is a guaranteed infinite loop — "completed" never
// advances — so it's rejected outright. Fixed-day callers (cmdItems,
// cmdProbability) pass false: an all-zero pool there is a legitimate "0 items"
// / "0% probability" answer, not an error.
func validatePool(pool *SamplePool, mode samplingMode, team []string, requireProgress bool) error {
	switch mode {
	case modeNamedTeam:
		teamTotal := 0
		for _, name := range team {
			samples, ok := pool.PerEngineer[name]
			if !ok {
				return fmt.Errorf("engineer %q not found in data", name)
			}
			if len(samples) == 0 {
				return fmt.Errorf("engineer %q has no sample days in the selected window (every day excluded?)", name)
			}
			teamTotal += sum(samples)
		}
		if requireProgress && teamTotal == 0 {
			return fmt.Errorf("team [%s] completed 0 items in the selected window; days-to-complete is undefined (they would never finish)", strings.Join(team, ", "))
		}
	case modeFullTeam:
		samples := pool.PerEngineer["__whole_team__"]
		if len(samples) == 0 {
			return fmt.Errorf("no sample days in the selected window (try a different -sample-start/-sample-end)")
		}
		if requireProgress && sum(samples) == 0 {
			return fmt.Errorf("whole-team throughput was 0 in the selected window; days-to-complete is undefined (it would never finish)")
		}
	default: // modeAnonymous
		samples := pool.GetCombinedSamples()
		if len(samples) == 0 {
			return fmt.Errorf("no completed items in the selected window (try a different -sample-start/-sample-end)")
		}
		if requireProgress && sum(samples) == 0 {
			return fmt.Errorf("0 items completed in the selected window; days-to-complete is undefined (it would never finish)")
		}
	}
	return nil
}

// simulateItemsInDays answers "how many items in `days` days?" for the given
// mode, dispatching to the right Simulate* engine.
func simulateItemsInDays(pool *SamplePool, mode samplingMode, team []string, engineers, days, numSimulations, numWorkers int, seed int64) []int {
	switch mode {
	case modeNamedTeam:
		return SimulateItemsInDaysPerEngineer(pool, team, days, numSimulations, numWorkers, seed)
	case modeFullTeam:
		return SimulateItemsInDays(pool.PerEngineer["__whole_team__"], 1, days, numSimulations, numWorkers, seed)
	default:
		return SimulateItemsInDays(pool.GetCombinedSamples(), engineers, days, numSimulations, numWorkers, seed)
	}
}

// simulateDaysToComplete answers "how many days to finish `items` items?" for
// the given mode, dispatching to the right Simulate* engine.
func simulateDaysToComplete(pool *SamplePool, mode samplingMode, team []string, engineers, items, numSimulations, numWorkers int, seed int64) []int {
	switch mode {
	case modeNamedTeam:
		return SimulateDaysToCompletePerEngineer(pool, team, items, numSimulations, numWorkers, seed)
	case modeFullTeam:
		return SimulateDaysToComplete(pool.PerEngineer["__whole_team__"], 1, items, numSimulations, numWorkers, seed)
	default:
		return SimulateDaysToComplete(pool.GetCombinedSamples(), engineers, items, numSimulations, numWorkers, seed)
	}
}

// --- Main ---

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: sim <command> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  items        How many items can N engineers complete in D days?\n")
	fmt.Fprintf(os.Stderr, "  days         How many days for N engineers to complete I items?\n")
	fmt.Fprintf(os.Stderr, "  probability  What is the probability of completing I items in D days?\n\n")
	fmt.Fprintf(os.Stderr, "Run 'sim <command> -help' for command-specific flags.\n")
}

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}

// resolveEndDate returns the end of the sample window. If -sample-end was
// explicitly passed, it's parsed as a calendar date (midnight, exclusive of
// that whole day). Otherwise it defaults to the current moment, so that
// today's already-completed work is included up to right now rather than
// being dropped entirely by a midnight-of-today cutoff.
func resolveEndDate(cmd *flag.FlagSet, sampleEnd string, now time.Time) (time.Time, error) {
	if !isFlagSet(cmd, "sample-end") {
		return now, nil
	}
	return parseDate(sampleEnd)
}

// daysBetween returns the number of per-day sample slots in [start, end).
// end is normally a calendar date at midnight, in which case that day is
// fully excluded. If end carries a time-of-day (e.g. it's "now"), the day
// it falls on is partially in range, so it gets one inclusive slot.
func daysBetween(start, end time.Time) int {
	endDay := end.Truncate(24 * time.Hour)
	days := int(endDay.Sub(start).Hours() / 24)
	if !end.Equal(endDay) {
		days++
	}
	return days
}

// loadPool builds a SamplePool by querying the SQLite store.
func loadPool(dbPath, exclusionsFile string, includeEngineers []string, startDate, endDate time.Time, wholeTeam bool) (*SamplePool, error) {
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	issues, err := store.CompletedBetween(context.Background(), startDate, endDate, includeEngineers)
	if err != nil {
		return nil, fmt.Errorf("querying db: %w", err)
	}

	engineerSeen := make(map[string]bool, len(issues))
	for _, it := range issues {
		engineerSeen[it.Assignee] = true
	}
	warnUnmatchedIncludes(includeEngineers, engineerSeen)

	exc, err := loadExclusions(exclusionsFile)
	if err != nil {
		return nil, err
	}

	records, skipped := validCompletions(issues)
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: skipped %d completed issue(s) with no assignee or completion date\n", skipped)
	}

	return buildPool(records, exc, startDate, endDate, wholeTeam), nil
}

// validCompletions reduces store rows to completion records, dropping any with
// no assignee or no completion instant and reporting how many were skipped.
//
// CompletedBetween already filters to completed, assigned issues, so in normal
// operation nothing is skipped. This is belt-and-suspenders: now that the store
// holds every issue, a future change to that query (or a new loader) could let
// an unassigned or never-completed issue through, and binning it as a "0
// completions" engineer-day would silently bias the forecast. Skipping it and
// returning a count lets the caller warn loudly instead.
func validCompletions(issues []linear.Issue) (records []completion, skipped int) {
	records = make([]completion, 0, len(issues))
	for _, it := range issues {
		if it.Assignee == "" || it.CompletedAt.IsZero() {
			skipped++
			continue
		}
		records = append(records, completion{Engineer: it.Assignee, CompletedAt: it.CompletedAt})
	}
	return records, skipped
}

// warnUnmatchedIncludes logs a warning for any name in includeEngineers that
// doesn't appear in seen, which usually indicates a typo in -include.
func warnUnmatchedIncludes(includeEngineers []string, seen map[string]bool) {
	for _, name := range includeEngineers {
		if !seen[name] {
			fmt.Fprintf(os.Stderr, "WARNING: -include engineer %q not found in data\n", name)
		}
	}
}

// isFlagSet reports whether a flag was explicitly provided on the command line.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// defaultDateRange returns a default date range of the last 6 months, formatted as YYYY-MM-DD.
func defaultDateRange() (start, end string) {
	now := time.Now().UTC()
	return now.AddDate(0, -6, 0).Format("2006-01-02"), now.Format("2006-01-02")
}

// resolveSeed returns randomSeed if -random-seed was explicitly set, otherwise
// a time-based seed so runs are non-deterministic by default.
func resolveSeed(cmd *flag.FlagSet, randomSeed int64, now time.Time) int64 {
	if isFlagSet(cmd, "random-seed") {
		return randomSeed
	}
	return now.UnixNano()
}

// cmdItems
func cmdItems(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("items", flag.ExitOnError)
	dbFile := cmd.String("db", "linear.db", "path to SQLite database")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	days := cmd.Int("days", 30, "number of days")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	goroutines := cmd.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	randomSeed := cmd.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	var percentiles intList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 5,25,50,75,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	var team stringList
	cmd.Var(&team, "team", "comma-separated list of specific engineer names to model individually")
	cmd.Parse(args)

	mode, err := resolveMode(isFlagSet(cmd, "engineers"), *wholeTeam, team)
	if err != nil {
		return err
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*dbFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	if err := validatePool(pool, mode, team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed, now)

	dist := simulateItemsInDays(pool, mode, team, *engineers, *days, *simulations, *goroutines, seed)
	fmt.Printf("%s, %d days -> how many items?\n", modeLabel(mode, team, *engineers), *days)

	if len(percentiles) == 0 {
		percentiles = intList{5, 25, 50, 75, 95}
	}
	for _, p := range percentiles {
		fmt.Printf("  %dth percentile: %d items\n", p, Percentile(dist, float64(p)))
	}
	return nil
}

func cmdDays(args []string) error {
	defaultSampleStart, defaultSampleEnd := defaultDateRange()
	cmd := flag.NewFlagSet("days", flag.ExitOnError)
	dbFile := cmd.String("db", "linear.db", "path to SQLite database")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	items := cmd.Int("items", 50, "number of items to complete")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	goroutines := cmd.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sampleStart := cmd.String("sample-start", defaultSampleStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultSampleEnd, "sample data end date (YYYY-MM-DD)")
	randomSeed := cmd.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	var percentiles intList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 50,75,85,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	var team stringList
	cmd.Var(&team, "team", "comma-separated list of specific engineer names to model individually")
	cmd.Parse(args)

	mode, err := resolveMode(isFlagSet(cmd, "engineers"), *wholeTeam, team)
	if err != nil {
		return err
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*dbFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	if err := validatePool(pool, mode, team, true); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed, now)

	dist := simulateDaysToComplete(pool, mode, team, *engineers, *items, *simulations, *goroutines, seed)
	fmt.Printf("%s, %d items -> how many days?\n", modeLabel(mode, team, *engineers), *items)

	if len(percentiles) == 0 {
		percentiles = intList{50, 75, 85, 95}
	}
	for _, p := range percentiles {
		fmt.Printf("  %dth percentile: %d days\n", p, Percentile(dist, float64(p)))
	}
	return nil
}

func cmdProbability(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("probability", flag.ExitOnError)
	dbFile := cmd.String("db", "linear.db", "path to SQLite database")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	days := cmd.Int("days", 30, "number of days")
	items := cmd.Int("items", -1, "number of items to complete (omit to show full distribution)")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	goroutines := cmd.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	randomSeed := cmd.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	var team stringList
	cmd.Var(&team, "team", "comma-separated list of specific engineer names to model individually")
	cmd.Parse(args)

	mode, err := resolveMode(isFlagSet(cmd, "engineers"), *wholeTeam, team)
	if err != nil {
		return err
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*dbFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	if err := validatePool(pool, mode, team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed, now)

	dist := simulateItemsInDays(pool, mode, team, *engineers, *days, *simulations, *goroutines, seed)
	modeDescription := modeLabel(mode, team, *engineers)

	if *items >= 0 {
		fmt.Printf("%s, %d days, %d items -> probability of completion?\n", modeDescription, *days, *items)
		fmt.Printf("  %.1f%%\n", probabilityAtLeast(dist, *items))
	} else {
		fmt.Printf("%s, %d days -> probability of completing N items\n", modeDescription, *days)
		for n := 1; ; n++ {
			p := probabilityAtLeast(dist, n)
			fmt.Printf("  %d items: %.1f%%\n", n, p)
			if p == 0 {
				break
			}
		}
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "items":
		err = cmdItems(os.Args[2:])
	case "days":
		err = cmdDays(os.Args[2:])
	case "probability":
		err = cmdProbability(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
