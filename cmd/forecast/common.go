package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/commondatageek/delivery-forecast/internal/linear"
	"github.com/commondatageek/delivery-forecast/internal/logx"
	"github.com/commondatageek/delivery-forecast/internal/simulate"
	"github.com/commondatageek/delivery-forecast/internal/sqlite"

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

// warnUnmatchedTypicalEngineers logs a warning for any name in typicalEngineers
// that doesn't appear in seen, which usually indicates a typo in
// -typical-engineers.
func warnUnmatchedTypicalEngineers(typicalEngineers []string, seen map[string]bool) {
	for _, name := range typicalEngineers {
		if !seen[name] {
			logx.Warnf("-typical-engineers engineer %q not found in data", name)
		}
	}
}

// loadExclusions reads and parses an exclusions JSON file. If the file does
// not exist, an empty Exclusions is returned without error.
func loadExclusions(path string) (simulate.Exclusions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return simulate.Exclusions{}, nil
		}
		return simulate.Exclusions{}, fmt.Errorf("reading exclusions file: %w", err)
	}
	return simulate.ParseExclusions(data)
}

// loadPool builds a SamplePool by querying the SQLite store.
func loadPool(dbPath, exclusionsFile string, typicalEngineers []string, startDate, endDate time.Time, wholeTeam bool) (poolData, error) {
	store, err := sqlite.OpenExisting(dbPath)
	if err != nil {
		return poolData{}, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	issues, err := store.CompletedBetween(context.Background(), startDate, endDate, typicalEngineers, nil)
	if err != nil {
		return poolData{}, fmt.Errorf("querying db: %w", err)
	}

	engineerSeen := make(map[string]bool, len(issues))
	for _, it := range issues {
		engineerSeen[it.Assignee] = true
	}
	warnUnmatchedTypicalEngineers(typicalEngineers, engineerSeen)

	exc, err := loadExclusions(exclusionsFile)
	if err != nil {
		return poolData{}, err
	}

	records, skipped := simulate.FilterInvalid(issuesToCompletions(issues))
	if skipped > 0 {
		logx.Warnf("skipped %d completed issue(s) with no assignee or completion date", skipped)
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

// warnIfBlendingTeams warns on stderr when no -teams filter was given but the
// store holds more than one team, so the caller knows the report silently
// blends every team's data together. A no-op when teams is non-empty (the user
// scoped explicitly) or the store holds at most one team. Used by the read-only
// report commands (count/aging/cfd); not linear sync, where an unset -teams is
// the intended "sync every team" default rather than an accidental blend.
func warnIfBlendingTeams(ctx context.Context, store *sqlite.Store, teams linear.TeamKeyList) error {
	if len(teams) > 0 {
		return nil
	}
	keys, err := store.DistinctTeamKeys(ctx)
	if err != nil {
		return err
	}
	if msg := blendingTeamsWarning(teams, keys); msg != "" {
		logx.Warnf("%s", msg)
	}
	return nil
}

// blendingTeamsWarning returns the "blending across all teams" warning line, or
// "" when no warning is warranted: either the user scoped explicitly (teams
// non-empty) or the store holds at most one team (allTeams). Kept pure (no I/O)
// so callers that already know the full team set — like count — can reuse the
// exact message without a second DistinctTeamKeys query, and so it is unit
// testable.
func blendingTeamsWarning(teams linear.TeamKeyList, allTeams []string) string {
	if len(teams) > 0 || len(allTeams) <= 1 {
		return ""
	}
	return fmt.Sprintf("no -teams filter given; blending data across all %d teams (%s)",
		len(allTeams), strings.Join(allTeams, ", "))
}

// addConfigFlag registers the -config flag used by every subcommand.
func addConfigFlag(fs *flag.FlagSet) *string {
	return fs.String("config", "", "path to a YAML config file supplying flag values (CLI flags override)")
}

// addTeamsFlag registers the -teams flag. usage is passed in whole (not just
// an example) because its wording differs meaningfully between commands that
// filter to a team set (aging/cfd/count) and linear sync, where -teams
// extends the candidate set rather than filtering.
func addTeamsFlag(fs *flag.FlagSet, usage string) *linear.TeamKeyList {
	var teams linear.TeamKeyList
	fs.Var(&teams, "teams", usage)
	return &teams
}

// simFlags bundles the sample-window/mode flag block shared by all four `sim`
// subcommands (items/days/probability/backtest). -items/-days/-percentile/
// -manifest differ per command and are declared there instead.
type simFlags struct {
	ExclusionsFile   *string
	Engineers        *int
	WholeTeam        *bool
	Simulations      *int
	Goroutines       *int
	SampleStart      *string
	SampleEnd        *string
	RandomSeed       *int64
	TypicalEngineers stringList
	Team             stringList
}

// addSimFlags registers simFlags's block onto fs and returns the bundle. The
// -simulations usage text defaults to "...to run"; callers with a different
// need (sim backtest runs simulations per backtested day) can override
// fs.Lookup("simulations").Usage afterward.
func addSimFlags(fs *flag.FlagSet) *simFlags {
	sf := &simFlags{}
	sf.ExclusionsFile = fs.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	sf.Engineers = fs.Int("engineers", 0, "number of (equivalent) engineers; one of -engineers, -team, or -whole-team is required")
	sf.WholeTeam = fs.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	sf.Simulations = fs.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sf.Goroutines = fs.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sf.SampleStart = fs.String("sample-start", "-3 months", `sample data start date (YYYY-MM-DD; or: yesterday, today, tomorrow, "-3 months")`)
	sf.SampleEnd = fs.String("sample-end", "now", `sample data end date (YYYY-MM-DD; or: now, yesterday, today, tomorrow, "-3 months")`)
	sf.RandomSeed = fs.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	fs.Var(&sf.TypicalEngineers, "typical-engineers", "comma-separated list of the team's typical engineers to build the sample pool from (default: all)")
	fs.Var(&sf.Team, "team", "comma-separated list of specific engineer names to model individually")
	return sf
}
