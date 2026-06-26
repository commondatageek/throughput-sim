package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"text/tabwriter"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

// backtestRow is one row in the per-day output table.
type backtestRow struct {
	Date      time.Time
	Completed int
	Remaining int
	Prob      float64
}

// countAsOf counts how many issues in the fixed set were completed by
// midnight of d, and how many were not yet completed but had been created
// by that point (and are therefore "remaining" work for that day).
func countAsOf(issues []linear.Issue, d time.Time) (completed, remaining int) {
	for _, it := range issues {
		completedByD := !it.CompletedAt.IsZero() && !it.CompletedAt.After(d)
		notYetCreated := !it.CreatedAt.IsZero() && it.CreatedAt.After(d)
		switch {
		case completedByD:
			completed++
		case notYetCreated:
			// neither column
		default:
			remaining++
		}
	}
	return
}

// earliestStartedAt returns the minimum non-zero StartedAt across all issues,
// or the zero time if none have one.
func earliestStartedAt(issues []linear.Issue) time.Time {
	var earliest time.Time
	for _, it := range issues {
		if it.StartedAt.IsZero() {
			continue
		}
		if earliest.IsZero() || it.StartedAt.Before(earliest) {
			earliest = it.StartedAt
		}
	}
	return earliest
}

// allCreatedBy reports whether every issue in the set had been created by d
// (i.e. nothing is still waiting to enter the backlog).
func allCreatedBy(issues []linear.Issue, d time.Time) bool {
	for _, it := range issues {
		if !it.CreatedAt.IsZero() && it.CreatedAt.After(d) {
			return false
		}
	}
	return true
}

func cmdBacktest(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("backtest", flag.ExitOnError)
	dbFile := cmd.String("db", "", "path to SQLite database")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	project := cmd.String("project", "", "project name to backtest (required)")
	milestone := cmd.String("milestone", "", "milestone name within the project (optional)")
	startDateStr := cmd.String("start-date", "", "first day to backtest, inclusive (YYYY-MM-DD); default: earliest started_at in the issue set")
	targetDateStr := cmd.String("target-date", "", "completion deadline to forecast against (YYYY-MM-DD, required)")
	engineers := cmd.Int("engineers", 3, "number of (equivalent) engineers")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run per backtested day")
	goroutines := cmd.Int("goroutines", runtime.NumCPU(), "number of parallel worker goroutines")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	randomSeed := cmd.Int64("random-seed", 0, "seed for the random number generator (default: time-based, non-deterministic)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	var team stringList
	cmd.Var(&team, "team", "comma-separated list of specific engineer names to model individually")
	format := cmd.String("format", "text", `output format: "text" or "csv"`)
	cmd.Parse(args)

	if *dbFile == "" {
		return fmt.Errorf("-db is required")
	}
	if *project == "" {
		return fmt.Errorf("-project is required")
	}
	if *targetDateStr == "" {
		return fmt.Errorf("-target-date is required")
	}
	if *format != "text" && *format != "csv" {
		return fmt.Errorf(`-format must be "text" or "csv"`)
	}

	targetDate, err := util.ParseDate(*targetDateStr)
	if err != nil {
		return fmt.Errorf("invalid -target-date: %w", err)
	}

	now := time.Now().UTC()
	sampleStartDate, err := util.ParseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start: %w", err)
	}
	sampleEndDate, err := resolveEndDate(cmd, *sampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end: %w", err)
	}

	mode, err := resolveMode(isFlagSet(cmd, "engineers"), *wholeTeam, team)
	if err != nil {
		return err
	}

	// Build the fixed sample pool once; reused for every backtested day.
	pd, err := loadPool(*dbFile, *exclusionsFile, include, sampleStartDate, sampleEndDate, *wholeTeam)
	if err != nil {
		return err
	}
	if err := validatePool(pd.Pool, mode, team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *randomSeed, now)

	// Fetch the tracked issue set once.
	store, err := sqlite.Open(*dbFile)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	issues, err := store.ProjectMilestoneIssues(context.Background(), *project, *milestone)
	if err != nil {
		return fmt.Errorf("querying issues: %w", err)
	}
	if len(issues) == 0 {
		if *milestone != "" {
			return fmt.Errorf("no issues found for project %q milestone %q — check spelling", *project, *milestone)
		}
		return fmt.Errorf("no issues found for project %q — check spelling", *project)
	}

	// Resolve start date: explicit flag wins; otherwise infer from the earliest
	// started_at across the issue set.
	var startDate time.Time
	if *startDateStr != "" {
		startDate, err = util.ParseDate(*startDateStr)
		if err != nil {
			return fmt.Errorf("invalid -start-date: %w", err)
		}
	} else {
		startDate = earliestStartedAt(issues)
		if startDate.IsZero() {
			return fmt.Errorf("no started_at found in issue set; provide -start-date explicitly")
		}
		// Truncate to calendar day.
		startDate = startDate.UTC().Truncate(24 * time.Hour)
	}

	if !targetDate.After(startDate) {
		return fmt.Errorf("-target-date must be after -start-date (inferred: %s)", startDate.Format("2006-01-02"))
	}

	// Daily backtest loop.
	var rows []backtestRow
	for d := startDate; !d.After(targetDate); d = d.AddDate(0, 0, 1) {
		completed, remaining := countAsOf(issues, d)
		daysToTarget := int(targetDate.Sub(d).Hours()/24) + 1

		var prob float64
		if remaining == 0 {
			prob = 100.0
		} else {
			dist := simulateItemsInDays(pd.Pool, mode, team, *engineers, daysToTarget, *simulations, *goroutines, seed)
			prob = probabilityAtLeast(dist, remaining)
		}
		rows = append(rows, backtestRow{d, completed, remaining, prob})

		if remaining == 0 && allCreatedBy(issues, d) {
			break
		}
	}

	switch *format {
	case "csv":
		return printBacktestCSV(rows)
	default:
		label := modeLabel(mode, team, *engineers)
		scope := *project
		if *milestone != "" {
			scope += " / " + *milestone
		}
		printBacktestText(rows, scope, label, len(issues), startDate, sampleStartDate, sampleEndDate)
	}
	return nil
}

func printBacktestText(rows []backtestRow, scope, label string, total int, startDate, sampleStart, sampleEnd time.Time) {
	fmt.Printf("Backtest: %s (%d issues, %s)\n", scope, total, label)
	fmt.Printf("Start date: %s  Sample window: %s – %s\n\n",
		startDate.Format("2006-01-02"), sampleStart.Format("2006-01-02"), sampleEnd.Format("2006-01-02"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tCOMPLETED\tREMAINING\tPROB")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%d\t%d\t%.1f%%\n",
			r.Date.Format("2006-01-02"), r.Completed, r.Remaining, r.Prob)
	}
	w.Flush()
}

func printBacktestCSV(rows []backtestRow) error {
	w := csv.NewWriter(os.Stdout)
	if err := w.Write([]string{"date", "completed", "remaining", "probability"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{
			r.Date.Format("2006-01-02"),
			strconv.Itoa(r.Completed),
			strconv.Itoa(r.Remaining),
			strconv.FormatFloat(r.Prob, 'f', 2, 64),
		}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
