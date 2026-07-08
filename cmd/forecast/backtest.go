package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/simulate"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

// issuesToBacktestItems converts linear.Issue records to simulate.BacktestItem.
func issuesToBacktestItems(issues []linear.Issue) []simulate.BacktestItem {
	items := make([]simulate.BacktestItem, len(issues))
	for i, it := range issues {
		items[i] = simulate.BacktestItem{
			CreatedAt:   it.CreatedAt,
			StartedAt:   it.StartedAt,
			CompletedAt: it.CompletedAt,
		}
	}
	return items
}

func cmdSimBacktest(args []string) error {
	cmd := flag.NewFlagSet("sim backtest", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	sf := addSimFlags(cmd)
	cmd.Lookup("simulations").Usage = "number of Monte Carlo simulations to run per backtested day"
	project := cmd.String("project", "", "project name to backtest (required)")
	milestone := cmd.String("milestone", "", "milestone name within the project (optional)")
	replayStartStr := cmd.String("replay-start-date", "", "first day to replay from, inclusive (YYYY-MM-DD); default: earliest started_at across the issue set")
	targetEndStr := cmd.String("target-end-date", "", "completion deadline to forecast against (YYYY-MM-DD, required)")
	format := cmd.String("format", "text", `output format: "text" or "csv"`)
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}
	if *project == "" {
		return fmt.Errorf("-project is required")
	}
	if *targetEndStr == "" {
		return fmt.Errorf("-target-end-date is required")
	}
	if *format != "text" && *format != "csv" {
		return fmt.Errorf(`-format must be "text" or "csv"`)
	}

	targetDate, err := util.ParseDate(*targetEndStr)
	if err != nil {
		return fmt.Errorf("invalid -target-end-date: %w", err)
	}

	now := time.Now().UTC()
	sampleStartDate, err := util.ParseDate(*sf.SampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start: %w", err)
	}
	sampleEndDate, err := resolveEndDate(cmd, *sf.SampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end: %w", err)
	}

	mode, err := simulate.ResolveMode(isFlagSet(cmd, "engineers"), *sf.WholeTeam, sf.Team)
	if err != nil {
		return err
	}

	// Build the fixed sample pool once; reused for every backtested day.
	pd, err := loadPool(*dbFile, *sf.ExclusionsFile, sf.Include, sampleStartDate, sampleEndDate, *sf.WholeTeam)
	if err != nil {
		return err
	}
	if err := simulate.ValidatePool(pd.Pool, mode, sf.Team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *sf.RandomSeed, now)

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

	btItems := issuesToBacktestItems(issues)

	// Resolve start date: explicit flag wins; otherwise infer from the earliest
	// started_at across the issue set.
	var startDate time.Time
	if *replayStartStr != "" {
		startDate, err = util.ParseDate(*replayStartStr)
		if err != nil {
			return fmt.Errorf("invalid -replay-start-date: %w", err)
		}
	} else {
		startDate = simulate.EarliestStartedAt(btItems)
		if startDate.IsZero() {
			return fmt.Errorf("no started_at found in issue set; provide -replay-start-date explicitly")
		}
		startDate = startDate.UTC().Truncate(24 * time.Hour)
	}

	if !targetDate.After(startDate) {
		return fmt.Errorf("-target-end-date must be after -replay-start-date (inferred: %s)", startDate.Format("2006-01-02"))
	}

	rows := simulate.RunBacktest(pd.Pool, btItems, startDate, targetDate, simulate.Params{
		Mode:        mode,
		Team:        sf.Team,
		Engineers:   *sf.Engineers,
		Simulations: *sf.Simulations,
		Workers:     *sf.Goroutines,
		Seed:        seed,
	})

	switch *format {
	case "csv":
		return printBacktestCSV(rows)
	default:
		label := simulate.ModeLabel(mode, sf.Team, *sf.Engineers)
		scope := *project
		if *milestone != "" {
			scope += " / " + *milestone
		}
		printBacktestText(rows, scope, label, len(issues), startDate, sampleStartDate, sampleEndDate)
	}
	return nil
}

func printBacktestText(rows []simulate.BacktestRow, scope, label string, total int, startDate, sampleStart, sampleEnd time.Time) {
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

func printBacktestCSV(rows []simulate.BacktestRow) error {
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
