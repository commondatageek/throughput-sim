package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/simulate"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"

	"github.com/mattn/go-isatty"
)

// --- Progress reporting ---

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

// --- Flag helpers ---

// isFlagSet reports whether a flag was explicitly provided on the command line
// or via a config file (ApplyConfig applies config values via fs.Set, so they
// register as set and drive the same presence-sensitive behavior as CLI flags).
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

// --- Shared flag registration ---
//
// These helpers just wrap repeated fs.String/fs.Var calls; each subcommand
// still owns its FlagSet, still calls Parse itself, and still applies
// -config and the isFlagSet presence checks in the same order as before.

// addDBFlag registers the -db flag used by every subcommand that reads a
// SQLite store.
func addDBFlag(fs *flag.FlagSet) *string {
	return fs.String("db", "", "path to SQLite database")
}

// requireDB reports an error if -db was left unset.
func requireDB(db *string) error {
	if *db == "" {
		return fmt.Errorf("-db is required")
	}
	return nil
}

// addConfigFlag registers the -config flag used by every subcommand.
func addConfigFlag(fs *flag.FlagSet) *string {
	return fs.String("config", "", "path to a YAML config file supplying flag values (CLI flags override)")
}

// addTeamsFlag registers the -teams flag. usage is passed in whole (not just
// an example) because its wording differs meaningfully between commands that
// filter to a team set (aging/cfd/count) and linear sync, where -teams
// extends the candidate set rather than filtering.
func addTeamsFlag(fs *flag.FlagSet, usage string) *linear.KeyList {
	var teams linear.KeyList
	fs.Var(&teams, "teams", usage)
	return &teams
}

// simFlags bundles the sample-window/mode flag block shared by all four `sim`
// subcommands (items/days/probability/backtest). -items/-days/-percentile/
// -manifest differ per command and are declared there instead.
type simFlags struct {
	ExclusionsFile *string
	Engineers      *int
	WholeTeam      *bool
	Simulations    *int
	Goroutines     *int
	SampleStart    *string
	SampleEnd      *string
	RandomSeed     *int64
	Include        stringList
	Team           stringList
}

// addSimFlags registers simFlags's block onto fs and returns the bundle. The
// -simulations usage text defaults to "...to run"; callers with a different
// need (sim backtest runs simulations per backtested day) can override
// fs.Lookup("simulations").Usage afterward.
func addSimFlags(fs *flag.FlagSet) *simFlags {
	defaultStart, defaultEnd := defaultDateRange()
	sf := &simFlags{}
	sf.ExclusionsFile = fs.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	sf.Engineers = fs.Int("engineers", 3, "number of (equivalent) engineers")
	sf.WholeTeam = fs.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	sf.Simulations = fs.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sf.Goroutines = fs.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sf.SampleStart = fs.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sf.SampleEnd = fs.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	sf.RandomSeed = fs.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	fs.Var(&sf.Include, "include", "comma-separated list of engineer names to include (default: all)")
	fs.Var(&sf.Team, "team", "comma-separated list of specific engineer names to model individually")
	return sf
}
