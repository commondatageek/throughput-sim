package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"forecasting/internal/sqlite"

	"github.com/mattn/go-isatty"
)

// stringList is a flag.Value for a comma-separated list of strings.
type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(val string) error {
	*s = nil
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

// percentileList is a flag.Value for a comma-separated list of ints.
type percentileList []int

func (p *percentileList) String() string {
	parts := make([]string, len(*p))
	for i, v := range *p {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func (p *percentileList) Set(s string) error {
	*p = nil
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		v, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("invalid percentile %q: %w", part, err)
		}
		*p = append(*p, v)
	}
	return nil
}

// --- Data loading ---

type RawIssue struct {
	Engineer    string `json:"engineer"`
	Title       string `json:"title"`
	CompletedAt string `json:"completed_at"` // RFC 3339 date string
}

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
// instant they completed something. Both the NDJSON and SQLite loaders reduce
// their source rows to a slice of these and hand them to buildPool.
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
func resolveEndDate(cmd *flag.FlagSet, sampleEnd string) (time.Time, error) {
	if !isFlagSet(cmd, "sample-end") {
		return time.Now().UTC(), nil
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

func loadPool(issuesFile, exclusionsFile string, includeEngineers []string, startDate, endDate time.Time, wholeTeam bool) (*SamplePool, error) {
	data, err := os.ReadFile(issuesFile)
	if err != nil {
		return nil, fmt.Errorf("reading issues file: %w", err)
	}
	var issues []RawIssue
	decoder := json.NewDecoder(bytes.NewReader(data))
	for decoder.More() {
		var issue RawIssue
		if err := decoder.Decode(&issue); err != nil {
			return nil, fmt.Errorf("decoding issue: %w", err)
		}
		issues = append(issues, issue)
	}

	exc, err := loadExclusions(exclusionsFile)
	if err != nil {
		return nil, err
	}

	// Collect unique engineers (respecting include filter), purely for the
	// typo warning below — buildPool derives the actual pool's engineer set
	// from in-window completions.
	includeSet := make(map[string]bool, len(includeEngineers))
	for _, name := range includeEngineers {
		includeSet[name] = true
	}
	engineerSeen := make(map[string]bool)
	for _, issue := range issues {
		engineerSeen[issue.Engineer] = true
	}
	warnUnmatchedIncludes(includeEngineers, engineerSeen)

	var records []completion
	for _, issue := range issues {
		if len(includeSet) > 0 && !includeSet[issue.Engineer] {
			continue
		}
		t, err := time.Parse(time.RFC3339, issue.CompletedAt)
		if err != nil {
			continue
		}
		records = append(records, completion{Engineer: issue.Engineer, CompletedAt: t})
	}

	return buildPool(records, exc, startDate, endDate, wholeTeam), nil
}

// loadPoolFromDB builds a SamplePool by querying the SQLite store instead of
// reading NDJSON. It is the preferred path when -db is provided.
func loadPoolFromDB(dbPath, exclusionsFile string, includeEngineers []string, startDate, endDate time.Time, wholeTeam bool) (*SamplePool, error) {
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	items, err := store.CompletedBetween(context.Background(), "linear", startDate, endDate, includeEngineers)
	if err != nil {
		return nil, fmt.Errorf("querying db: %w", err)
	}

	engineerSeen := make(map[string]bool, len(items))
	for _, it := range items {
		engineerSeen[it.Assignee] = true
	}
	warnUnmatchedIncludes(includeEngineers, engineerSeen)

	exc, err := loadExclusions(exclusionsFile)
	if err != nil {
		return nil, err
	}

	records := make([]completion, len(items))
	for i, it := range items {
		records[i] = completion{Engineer: it.Assignee, CompletedAt: it.CompletedAt}
	}

	return buildPool(records, exc, startDate, endDate, wholeTeam), nil
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

// resolvePool picks the DB-backed loader when -db was explicitly set or when
// -issues was not explicitly set (so -db is the new default). Falls back to
// the NDJSON loader only when -issues is explicitly provided.
func resolvePool(cmd *flag.FlagSet, dbPath, issuesFile, exclusionsFile string, include []string, startDate, endDate time.Time, wholeTeam bool) (*SamplePool, error) {
	if isFlagSet(cmd, "issues") {
		return loadPool(issuesFile, exclusionsFile, include, startDate, endDate, wholeTeam)
	}
	return loadPoolFromDB(dbPath, exclusionsFile, include, startDate, endDate, wholeTeam)
}

// defaultDateRange returns a default date range of the last 6 months, formatted as YYYY-MM-DD.
func defaultDateRange() (start, end string) {
	now := time.Now().UTC()
	return now.AddDate(0, -6, 0).Format("2006-01-02"), now.Format("2006-01-02")
}

// resolveSeed returns randomSeed if -random-seed was explicitly set, otherwise
// a time-based seed so runs are non-deterministic by default.
func resolveSeed(cmd *flag.FlagSet, randomSeed int64) int64 {
	if isFlagSet(cmd, "random-seed") {
		return randomSeed
	}
	return time.Now().UnixNano()
}

// cmdItems
func cmdItems(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("items", flag.ExitOnError)
	dbFile := cmd.String("db", "items.db", "path to SQLite database (default source)")
	issuesFile := cmd.String("issues", "issues.json", "path to NDJSON issues file (overrides -db)")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	days := cmd.Int("days", 30, "number of days")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	goroutines := cmd.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	randomSeed := cmd.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	var percentiles percentileList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 5,25,50,75,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	var team stringList
	cmd.Var(&team, "team", "comma-separated list of specific engineer names to model individually")
	cmd.Parse(args)

	if *wholeTeam && isFlagSet(cmd, "engineers") {
		return fmt.Errorf("-whole-team and -engineers are mutually exclusive")
	}
	if *wholeTeam && len(team) > 0 {
		return fmt.Errorf("-whole-team and -team are mutually exclusive")
	}
	if isFlagSet(cmd, "engineers") && len(team) > 0 {
		return fmt.Errorf("-engineers and -team are mutually exclusive")
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := resolveEndDate(cmd, *sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := resolvePool(cmd, *dbFile, *issuesFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed)

	var dist []int
	if len(team) > 0 {
		// Named engineers mode
		for _, name := range team {
			if _, ok := pool.PerEngineer[name]; !ok {
				return fmt.Errorf("engineer %q not found in data", name)
			}
		}
		dist = SimulateItemsInDaysPerEngineer(pool, team, *days, *simulations, *goroutines, seed)
		fmt.Printf("Team [%s], %d days -> how many items?\n", strings.Join(team, ", "), *days)
	} else if *wholeTeam {
		// Whole team mode
		dist = SimulateItemsInDays(pool.PerEngineer["__whole_team__"], 1, *days, *simulations, *goroutines, seed)
		fmt.Printf("whole-team throughput, %d days -> how many items?\n", *days)
	} else {
		// Anonymous engineers mode
		combinedSamples := pool.GetCombinedSamples()
		dist = SimulateItemsInDays(combinedSamples, *engineers, *days, *simulations, *goroutines, seed)
		fmt.Printf("%d engineers, %d days -> how many items?\n", *engineers, *days)
	}

	if len(percentiles) == 0 {
		percentiles = percentileList{5, 25, 50, 75, 95}
	}
	for _, p := range percentiles {
		fmt.Printf("  %dth percentile: %d items\n", p, Percentile(dist, float64(p)))
	}
	return nil
}

func cmdDays(args []string) error {
	defaultSampleStart, defaultSampleEnd := defaultDateRange()
	cmd := flag.NewFlagSet("days", flag.ExitOnError)
	dbFile := cmd.String("db", "items.db", "path to SQLite database (default source)")
	issuesFile := cmd.String("issues", "issues.json", "path to NDJSON issues file (overrides -db)")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	items := cmd.Int("items", 50, "number of items to complete")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	goroutines := cmd.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sampleStart := cmd.String("sample-start", defaultSampleStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultSampleEnd, "sample data end date (YYYY-MM-DD)")
	randomSeed := cmd.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	var percentiles percentileList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 50,75,85,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	var team stringList
	cmd.Var(&team, "team", "comma-separated list of specific engineer names to model individually")
	cmd.Parse(args)

	if *wholeTeam && isFlagSet(cmd, "engineers") {
		return fmt.Errorf("-whole-team and -engineers are mutually exclusive")
	}
	if *wholeTeam && len(team) > 0 {
		return fmt.Errorf("-whole-team and -team are mutually exclusive")
	}
	if isFlagSet(cmd, "engineers") && len(team) > 0 {
		return fmt.Errorf("-engineers and -team are mutually exclusive")
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := resolveEndDate(cmd, *sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := resolvePool(cmd, *dbFile, *issuesFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed)

	var dist []int
	if len(team) > 0 {
		// Named engineers mode
		for _, name := range team {
			if _, ok := pool.PerEngineer[name]; !ok {
				return fmt.Errorf("engineer %q not found in data", name)
			}
		}
		dist = SimulateDaysToCompletePerEngineer(pool, team, *items, *simulations, *goroutines, seed)
		fmt.Printf("Team [%s], %d items -> how many days?\n", strings.Join(team, ", "), *items)
	} else if *wholeTeam {
		// Whole team mode
		dist = SimulateDaysToComplete(pool.PerEngineer["__whole_team__"], 1, *items, *simulations, *goroutines, seed)
		fmt.Printf("whole-team throughput, %d items -> how many days?\n", *items)
	} else {
		// Anonymous engineers mode
		combinedSamples := pool.GetCombinedSamples()
		dist = SimulateDaysToComplete(combinedSamples, *engineers, *items, *simulations, *goroutines, seed)
		fmt.Printf("%d engineers, %d items -> how many days?\n", *engineers, *items)
	}

	if len(percentiles) == 0 {
		percentiles = percentileList{50, 75, 85, 95}
	}
	for _, p := range percentiles {
		fmt.Printf("  %dth percentile: %d days\n", p, Percentile(dist, float64(p)))
	}
	return nil
}

func cmdProbability(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("probability", flag.ExitOnError)
	dbFile := cmd.String("db", "items.db", "path to SQLite database (default source)")
	issuesFile := cmd.String("issues", "issues.json", "path to NDJSON issues file (overrides -db)")
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

	if *wholeTeam && isFlagSet(cmd, "engineers") {
		return fmt.Errorf("-whole-team and -engineers are mutually exclusive")
	}
	if *wholeTeam && len(team) > 0 {
		return fmt.Errorf("-whole-team and -team are mutually exclusive")
	}
	if isFlagSet(cmd, "engineers") && len(team) > 0 {
		return fmt.Errorf("-engineers and -team are mutually exclusive")
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := resolveEndDate(cmd, *sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := resolvePool(cmd, *dbFile, *issuesFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed)

	var dist []int
	var modeDescription string

	if len(team) > 0 {
		// Named engineers mode
		for _, name := range team {
			if _, ok := pool.PerEngineer[name]; !ok {
				return fmt.Errorf("engineer %q not found in data", name)
			}
		}
		dist = SimulateItemsInDaysPerEngineer(pool, team, *days, *simulations, *goroutines, seed)
		modeDescription = fmt.Sprintf("Team [%s]", strings.Join(team, ", "))
	} else if *wholeTeam {
		// Whole team mode
		dist = SimulateItemsInDays(pool.PerEngineer["__whole_team__"], 1, *days, *simulations, *goroutines, seed)
		modeDescription = "whole-team throughput"
	} else {
		// Anonymous engineers mode
		combinedSamples := pool.GetCombinedSamples()
		dist = SimulateItemsInDays(combinedSamples, *engineers, *days, *simulations, *goroutines, seed)
		modeDescription = fmt.Sprintf("%d engineers", *engineers)
	}

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
