package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/simulate"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"

	"github.com/mattn/go-isatty"
)

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

func (b *progressBar) update(done, _ int) {
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

// --- Pool loading ---

// poolData bundles the built pool with the raw inputs that produced it, so a
// run manifest can record exactly what fed the simulation.
type poolData struct {
	Pool       *simulate.SamplePool
	Issues     []linear.Issue
	Exclusions simulate.Exclusions
	Skipped    int
}

// issuesToCompletions converts linear.Issue records to simulate.Completion.
// No filtering is performed; call simulate.FilterInvalid on the result.
func issuesToCompletions(issues []linear.Issue) []simulate.Completion {
	records := make([]simulate.Completion, len(issues))
	for i, it := range issues {
		records[i] = simulate.Completion{Engineer: it.Assignee, CompletedAt: it.CompletedAt}
	}
	return records
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

// loadPool builds a SamplePool by querying the SQLite store.
func loadPool(dbPath, exclusionsFile string, includeEngineers []string, startDate, endDate time.Time, wholeTeam bool) (poolData, error) {
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return poolData{}, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	issues, err := store.CompletedBetween(context.Background(), startDate, endDate, includeEngineers, nil)
	if err != nil {
		return poolData{}, fmt.Errorf("querying db: %w", err)
	}

	engineerSeen := make(map[string]bool, len(issues))
	for _, it := range issues {
		engineerSeen[it.Assignee] = true
	}
	warnUnmatchedIncludes(includeEngineers, engineerSeen)

	exc, err := simulate.LoadExclusions(exclusionsFile)
	if err != nil {
		return poolData{}, err
	}

	records, skipped := simulate.FilterInvalid(issuesToCompletions(issues))
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: skipped %d completed issue(s) with no assignee or completion date\n", skipped)
	}

	return poolData{
		Pool:       simulate.BuildPool(records, exc, startDate, endDate, wholeTeam),
		Issues:     issues,
		Exclusions: exc,
		Skipped:    skipped,
	}, nil
}

// --- Helpers ---

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

// resolveRelativeDate parses s as a calendar date, accepting YYYY-MM-DD or
// the relative keywords today and tomorrow.
func resolveRelativeDate(s string, now time.Time) (time.Time, error) {
	y, m, d := now.Local().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	switch strings.ToLower(s) {
	case "today":
		return today, nil
	case "tomorrow":
		return today.AddDate(0, 0, 1), nil
	default:
		return util.ParseDate(s)
	}
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
	return util.ParseDate(sampleEnd)
}

// resolveSeed returns randomSeed if -random-seed was explicitly set, otherwise
// a time-based seed so runs are non-deterministic by default.
func resolveSeed(cmd *flag.FlagSet, randomSeed int64, now time.Time) int64 {
	if isFlagSet(cmd, "random-seed") {
		return randomSeed
	}
	return now.UnixNano()
}

// --- Main ---

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: sim <command> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  items        How many items can N engineers complete in D days?\n")
	fmt.Fprintf(os.Stderr, "  days         How many days for N engineers to complete I items?\n")
	fmt.Fprintf(os.Stderr, "  probability  What is the probability of completing I items in D days?\n")
	fmt.Fprintf(os.Stderr, "  backtest     Replay probability forecasts day-by-day for a project/milestone.\n\n")
	fmt.Fprintf(os.Stderr, "Run 'sim <command> -help' for command-specific flags.\n")
}

func cmdItems(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("items", flag.ExitOnError)
	dbFile := cmd.String("db", "", "path to SQLite database")
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
	manifestFile := cmd.String("manifest", "", `write a run-provenance JSON manifest to this path ("-" for stdout)`)
	configFile := cmd.String("config", "", "path to a YAML config file supplying flag values (CLI flags override)")
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if *dbFile == "" {
		return fmt.Errorf("-db is required")
	}

	mode, err := simulate.ResolveMode(isFlagSet(cmd, "engineers"), *wholeTeam, team)
	if err != nil {
		return err
	}

	startDate, err := util.ParseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	loaded, err := loadPool(*dbFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	pool := loaded.Pool
	if err := simulate.ValidatePool(pool, mode, team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed, now)

	if len(percentiles) == 0 {
		percentiles = intList{5, 25, 50, 75, 95}
	}

	if err := writeManifest(*manifestFile, manifestInputs{
		Subcommand: "items", Cmd: cmd, Mode: mode, Team: team, Include: include,
		Engineers: *engineers, WholeTeam: *wholeTeam, Seed: seed,
		SampleStart: startDate, SampleEnd: endDate,
		DBPath: *dbFile, ExclusionsPath: *exclusionsFile,
		Exclusions: loaded.Exclusions, Pool: pool, Issues: loaded.Issues, Skipped: loaded.Skipped,
		Extra: map[string]any{"effective_percentiles": []int(percentiles)},
	}); err != nil {
		return err
	}

	bar := newProgressBar(*simulations)
	dist := simulate.ItemsInDays(pool, simulate.Params{
		Mode:        mode,
		Team:        team,
		Engineers:   *engineers,
		Days:        *days,
		Simulations: *simulations,
		Workers:     *goroutines,
		Seed:        seed,
		Progress:    bar.update,
	})
	fmt.Printf("%s, %d days -> how many items?\n", simulate.ModeLabel(mode, team, *engineers), *days)

	for _, p := range percentiles {
		fmt.Printf("  %dth percentile: %d items\n", p, util.PercentileValue(dist, float64(p)))
	}
	return nil
}

// printTrajectoryReport prints the grouped trajectory report for `sim days
// -items g1,g2,...`: one row per group plus a Total row, with per-percentile
// Days/Date columns. All thresholds are simulated with the same seed (see
// simulate.ComputeTrajectoryTable) so the report's invariants hold.
func printTrajectoryReport(pool *simulate.SamplePool, mode simulate.Mode, team []string, engineers int, seed int64, simulations, goroutines int, groups, percentiles []int, targetStartDate time.Time) {
	cum := make([]int, len(groups))
	total := 0
	for g, n := range groups {
		total += n
		cum[g] = total
	}

	dists := make([][]int, len(groups))
	for g, threshold := range cum {
		dists[g] = simulate.DaysToComplete(pool, simulate.Params{
			Mode:        mode,
			Team:        team,
			Engineers:   engineers,
			Items:       threshold,
			Simulations: simulations,
			Workers:     goroutines,
			Seed:        seed,
		})
	}
	cells, totals := simulate.ComputeTrajectoryTable(dists, percentiles)

	fmt.Printf("%s, starting %s -> grouped trajectory\n\n", simulate.ModeLabel(mode, team, engineers), targetStartDate.Format("2006-01-02"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	pctRow := []string{"", ""}
	header := []string{"Group", "Items"}
	for _, p := range percentiles {
		pctRow = append(pctRow, fmt.Sprintf("p%d", p), "")
		header = append(header, "Days", "Date")
	}
	fmt.Fprintln(w, strings.Join(pctRow, "\t"))
	fmt.Fprintln(w, strings.Join(header, "\t"))

	for g := range groups {
		row := []string{fmt.Sprintf("Group %d", g+1), fmt.Sprintf("%d", groups[g])}
		for pi := range percentiles {
			cell := cells[g][pi]
			date := targetStartDate.AddDate(0, 0, cell.CumulativeDays)
			row = append(row, fmt.Sprintf("%d", cell.MarginalDays), date.Format("2006-01-02 (Mon)"))
		}
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}

	totalRow := []string{"Total", fmt.Sprintf("%d", total)}
	for pi := range percentiles {
		days := totals[pi]
		date := targetStartDate.AddDate(0, 0, days)
		totalRow = append(totalRow, fmt.Sprintf("%d", days), date.Format("2006-01-02 (Mon)"))
	}
	fmt.Fprintln(w, strings.Join(totalRow, "\t"))

	w.Flush()
}

func cmdDays(args []string) error {
	defaultSampleStart, defaultSampleEnd := defaultDateRange()
	cmd := flag.NewFlagSet("days", flag.ExitOnError)
	dbFile := cmd.String("db", "", "path to SQLite database")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	items := intList{50}
	cmd.Var(&items, "items", "number of items to complete; comma-separated for a grouped trajectory report (e.g. 13,12,9)")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	goroutines := cmd.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sampleStart := cmd.String("sample-start", defaultSampleStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultSampleEnd, "sample data end date (YYYY-MM-DD)")
	randomSeed := cmd.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	targetStartStr := cmd.String("target-start-date", "today", "forecast start date used to compute calendar dates (YYYY-MM-DD, or: today, tomorrow)")
	var percentiles intList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 50,75,85,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	var team stringList
	cmd.Var(&team, "team", "comma-separated list of specific engineer names to model individually")
	manifestFile := cmd.String("manifest", "", `write a run-provenance JSON manifest to this path ("-" for stdout)`)
	configFile := cmd.String("config", "", "path to a YAML config file supplying flag values (CLI flags override)")
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if *dbFile == "" {
		return fmt.Errorf("-db is required")
	}

	mode, err := simulate.ResolveMode(isFlagSet(cmd, "engineers"), *wholeTeam, team)
	if err != nil {
		return err
	}

	startDate, err := util.ParseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	for _, n := range items {
		if n <= 0 {
			return fmt.Errorf("-items: group sizes must be positive, got %d", n)
		}
	}

	loaded, err := loadPool(*dbFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	pool := loaded.Pool
	if err := simulate.ValidatePool(pool, mode, team, true); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed, now)

	targetStartDate, err := resolveRelativeDate(*targetStartStr, now)
	if err != nil {
		return fmt.Errorf("invalid -target-start-date: %w", err)
	}

	if len(percentiles) == 0 {
		percentiles = intList{50, 75, 85, 95}
	}

	if err := writeManifest(*manifestFile, manifestInputs{
		Subcommand: "days", Cmd: cmd, Mode: mode, Team: team, Include: include,
		Engineers: *engineers, WholeTeam: *wholeTeam, Seed: seed,
		SampleStart: startDate, SampleEnd: endDate,
		DBPath: *dbFile, ExclusionsPath: *exclusionsFile,
		Exclusions: loaded.Exclusions, Pool: pool, Issues: loaded.Issues, Skipped: loaded.Skipped,
		Extra: map[string]any{"effective_percentiles": []int(percentiles)},
	}); err != nil {
		return err
	}

	if len(items) > 1 {
		printTrajectoryReport(pool, mode, team, *engineers, seed, *simulations, *goroutines, items, percentiles, targetStartDate)
		return nil
	}

	bar := newProgressBar(*simulations)
	dist := simulate.DaysToComplete(pool, simulate.Params{
		Mode:        mode,
		Team:        team,
		Engineers:   *engineers,
		Items:       items[0],
		Simulations: *simulations,
		Workers:     *goroutines,
		Seed:        seed,
		Progress:    bar.update,
	})
	fmt.Printf("%s, %d items -> how many days?\n\n", simulate.ModeLabel(mode, team, *engineers), items[0])

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Percentile\tDays\tDate")
	for _, p := range percentiles {
		days := util.PercentileValue(dist, float64(p))
		date := targetStartDate.AddDate(0, 0, days)
		fmt.Fprintf(w, "p%d\t%d\t%s\n", p, days, date.Format("2006-01-02 Mon"))
	}
	w.Flush()
	return nil
}

func cmdProbability(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("probability", flag.ExitOnError)
	dbFile := cmd.String("db", "", "path to SQLite database")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	days := cmd.Int("days", 0, "number of days; mutually exclusive with -target-end-date, one must be given")
	targetStartStr := cmd.String("target-start-date", "tomorrow", `start of the target window (YYYY-MM-DD, or: today, tomorrow); default: tomorrow`)
	targetEndStr := cmd.String("target-end-date", "", "end of the target window (YYYY-MM-DD, or: today, tomorrow); mutually exclusive with -days, one must be given")
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
	manifestFile := cmd.String("manifest", "", `write a run-provenance JSON manifest to this path ("-" for stdout)`)
	configFile := cmd.String("config", "", "path to a YAML config file supplying flag values (CLI flags override)")
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if *dbFile == "" {
		return fmt.Errorf("-db is required")
	}

	mode, err := simulate.ResolveMode(isFlagSet(cmd, "engineers"), *wholeTeam, team)
	if err != nil {
		return err
	}

	daysSet := isFlagSet(cmd, "days")
	targetEndSet := isFlagSet(cmd, "target-end-date")
	if daysSet && targetEndSet {
		return fmt.Errorf("-days and -target-end-date are mutually exclusive")
	}
	if !daysSet && !targetEndSet {
		return fmt.Errorf("one of -days or -target-end-date must be provided")
	}

	startDate, err := util.ParseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	effectiveDays := *days
	var targetStart, targetEnd time.Time
	if targetEndSet {
		targetStart, err = resolveRelativeDate(*targetStartStr, now)
		if err != nil {
			return fmt.Errorf("invalid -target-start-date: %w", err)
		}
		targetEnd, err = resolveRelativeDate(*targetEndStr, now)
		if err != nil {
			return fmt.Errorf("invalid -target-end-date: %w", err)
		}
		if !targetEnd.After(targetStart) {
			return fmt.Errorf("-target-end-date must be after -target-start-date")
		}
		effectiveDays = int(targetEnd.Sub(targetStart).Hours()/24) + 1
	}

	loaded, err := loadPool(*dbFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	pool := loaded.Pool
	if err := simulate.ValidatePool(pool, mode, team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed, now)

	manifestExtra := map[string]any{}
	if targetEndSet {
		manifestExtra["target_start_date"] = targetStart.Format("2006-01-02")
		manifestExtra["target_end_date"] = targetEnd.Format("2006-01-02")
		manifestExtra["effective_days"] = effectiveDays
	}
	if err := writeManifest(*manifestFile, manifestInputs{
		Subcommand: "probability", Cmd: cmd, Mode: mode, Team: team, Include: include,
		Engineers: *engineers, WholeTeam: *wholeTeam, Seed: seed,
		SampleStart: startDate, SampleEnd: endDate,
		DBPath: *dbFile, ExclusionsPath: *exclusionsFile,
		Exclusions: loaded.Exclusions, Pool: pool, Issues: loaded.Issues, Skipped: loaded.Skipped,
		Extra: manifestExtra,
	}); err != nil {
		return err
	}

	bar := newProgressBar(*simulations)
	dist := simulate.ItemsInDays(pool, simulate.Params{
		Mode:        mode,
		Team:        team,
		Engineers:   *engineers,
		Days:        effectiveDays,
		Simulations: *simulations,
		Workers:     *goroutines,
		Seed:        seed,
		Progress:    bar.update,
	})
	modeDescription := simulate.ModeLabel(mode, team, *engineers)

	var windowDescription string
	if targetEndSet {
		windowDescription = fmt.Sprintf("%s to %s (%d days)", targetStart.Format("2006-01-02"), targetEnd.Format("2006-01-02"), effectiveDays)
	} else {
		windowDescription = fmt.Sprintf("%d days", effectiveDays)
	}

	if *items >= 0 {
		fmt.Printf("%s, %s, %d items -> probability of completion?\n", modeDescription, windowDescription, *items)
		fmt.Printf("  %.1f%%\n", simulate.ProbabilityAtLeast(dist, *items))
	} else {
		fmt.Printf("%s, %s -> probability of completing N items\n", modeDescription, windowDescription)
		for n := 1; ; n++ {
			p := simulate.ProbabilityAtLeast(dist, n)
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
	case "backtest":
		err = cmdBacktest(os.Args[2:])
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
