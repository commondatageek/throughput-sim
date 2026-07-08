package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"forecasting/internal/simulate"
	"forecasting/internal/util"
)

func cmdSimItems(args []string) error {
	cmd := flag.NewFlagSet("sim items", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	sf := addSimFlags(cmd)
	days := cmd.Int("days", 30, "number of days")
	var percentiles intList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 5,25,50,75,95)")
	manifestFile := cmd.String("manifest", "", `write a run-provenance JSON manifest to this path ("-" for stdout)`)
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	mode, err := simulate.ResolveMode(isFlagSet(cmd, "engineers"), *sf.WholeTeam, sf.Team)
	if err != nil {
		return err
	}

	startDate, err := util.ParseDate(*sf.SampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sf.SampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	loaded, err := loadPool(*dbFile, *sf.ExclusionsFile, sf.Include, startDate, endDate, *sf.WholeTeam)
	if err != nil {
		return err
	}
	pool := loaded.Pool
	if err := simulate.ValidatePool(pool, mode, sf.Team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *sf.RandomSeed, now)

	if len(percentiles) == 0 {
		percentiles = intList{5, 25, 50, 75, 95}
	}

	if err := writeManifest(*manifestFile, manifestInputs{
		Subcommand: "sim items", Cmd: cmd, Mode: mode, Team: sf.Team, Include: sf.Include,
		Engineers: *sf.Engineers, WholeTeam: *sf.WholeTeam, Seed: seed,
		SampleStart: startDate, SampleEnd: endDate,
		DBPath: *dbFile, ExclusionsPath: *sf.ExclusionsFile,
		Exclusions: loaded.Exclusions, Pool: pool, Issues: loaded.Issues, Skipped: loaded.Skipped,
		Extra: map[string]any{"effective_percentiles": []int(percentiles)},
	}); err != nil {
		return err
	}

	bar := newProgressBar(*sf.Simulations)
	dist := simulate.ItemsInDays(pool, simulate.Params{
		Mode:        mode,
		Team:        sf.Team,
		Engineers:   *sf.Engineers,
		Days:        *days,
		Simulations: *sf.Simulations,
		Workers:     *sf.Goroutines,
		Seed:        seed,
		Progress:    bar.update,
	})
	fmt.Printf("%s, %d days -> how many items?\n", simulate.ModeLabel(mode, sf.Team, *sf.Engineers), *days)

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

func cmdSimDays(args []string) error {
	cmd := flag.NewFlagSet("sim days", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	sf := addSimFlags(cmd)
	items := intList{50}
	cmd.Var(&items, "items", "number of items to complete; comma-separated for a grouped trajectory report (e.g. 13,12,9)")
	targetStartStr := cmd.String("target-start-date", "today", "forecast start date used to compute calendar dates (YYYY-MM-DD, or: today, tomorrow)")
	var percentiles intList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 50,75,85,95)")
	manifestFile := cmd.String("manifest", "", `write a run-provenance JSON manifest to this path ("-" for stdout)`)
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	mode, err := simulate.ResolveMode(isFlagSet(cmd, "engineers"), *sf.WholeTeam, sf.Team)
	if err != nil {
		return err
	}

	startDate, err := util.ParseDate(*sf.SampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sf.SampleEnd, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	for _, n := range items {
		if n <= 0 {
			return fmt.Errorf("-items: group sizes must be positive, got %d", n)
		}
	}

	loaded, err := loadPool(*dbFile, *sf.ExclusionsFile, sf.Include, startDate, endDate, *sf.WholeTeam)
	if err != nil {
		return err
	}
	pool := loaded.Pool
	if err := simulate.ValidatePool(pool, mode, sf.Team, true); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *sf.RandomSeed, now)

	targetStartDate, err := resolveRelativeDate(*targetStartStr, now)
	if err != nil {
		return fmt.Errorf("invalid -target-start-date: %w", err)
	}

	if len(percentiles) == 0 {
		percentiles = intList{50, 75, 85, 95}
	}

	if err := writeManifest(*manifestFile, manifestInputs{
		Subcommand: "sim days", Cmd: cmd, Mode: mode, Team: sf.Team, Include: sf.Include,
		Engineers: *sf.Engineers, WholeTeam: *sf.WholeTeam, Seed: seed,
		SampleStart: startDate, SampleEnd: endDate,
		DBPath: *dbFile, ExclusionsPath: *sf.ExclusionsFile,
		Exclusions: loaded.Exclusions, Pool: pool, Issues: loaded.Issues, Skipped: loaded.Skipped,
		Extra: map[string]any{"effective_percentiles": []int(percentiles)},
	}); err != nil {
		return err
	}

	if len(items) > 1 {
		printTrajectoryReport(pool, mode, sf.Team, *sf.Engineers, seed, *sf.Simulations, *sf.Goroutines, items, percentiles, targetStartDate)
		return nil
	}

	bar := newProgressBar(*sf.Simulations)
	dist := simulate.DaysToComplete(pool, simulate.Params{
		Mode:        mode,
		Team:        sf.Team,
		Engineers:   *sf.Engineers,
		Items:       items[0],
		Simulations: *sf.Simulations,
		Workers:     *sf.Goroutines,
		Seed:        seed,
		Progress:    bar.update,
	})
	fmt.Printf("%s, %d items -> how many days?\n\n", simulate.ModeLabel(mode, sf.Team, *sf.Engineers), items[0])

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

func cmdSimProbability(args []string) error {
	cmd := flag.NewFlagSet("sim probability", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	sf := addSimFlags(cmd)
	days := cmd.Int("days", 0, "number of days; mutually exclusive with -target-end-date, one must be given")
	targetStartStr := cmd.String("target-start-date", "tomorrow", `start of the target window (YYYY-MM-DD, or: today, tomorrow); default: tomorrow`)
	targetEndStr := cmd.String("target-end-date", "", "end of the target window (YYYY-MM-DD, or: today, tomorrow); mutually exclusive with -days, one must be given")
	items := cmd.Int("items", -1, "number of items to complete (omit to show full distribution)")
	manifestFile := cmd.String("manifest", "", `write a run-provenance JSON manifest to this path ("-" for stdout)`)
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	mode, err := simulate.ResolveMode(isFlagSet(cmd, "engineers"), *sf.WholeTeam, sf.Team)
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

	startDate, err := util.ParseDate(*sf.SampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	now := time.Now().UTC()
	endDate, err := resolveEndDate(cmd, *sf.SampleEnd, now)
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

	loaded, err := loadPool(*dbFile, *sf.ExclusionsFile, sf.Include, startDate, endDate, *sf.WholeTeam)
	if err != nil {
		return err
	}
	pool := loaded.Pool
	if err := simulate.ValidatePool(pool, mode, sf.Team, false); err != nil {
		return err
	}
	seed := resolveSeed(cmd, *sf.RandomSeed, now)

	manifestExtra := map[string]any{}
	if targetEndSet {
		manifestExtra["target_start_date"] = targetStart.Format("2006-01-02")
		manifestExtra["target_end_date"] = targetEnd.Format("2006-01-02")
		manifestExtra["effective_days"] = effectiveDays
	}
	if err := writeManifest(*manifestFile, manifestInputs{
		Subcommand: "sim probability", Cmd: cmd, Mode: mode, Team: sf.Team, Include: sf.Include,
		Engineers: *sf.Engineers, WholeTeam: *sf.WholeTeam, Seed: seed,
		SampleStart: startDate, SampleEnd: endDate,
		DBPath: *dbFile, ExclusionsPath: *sf.ExclusionsFile,
		Exclusions: loaded.Exclusions, Pool: pool, Issues: loaded.Issues, Skipped: loaded.Skipped,
		Extra: manifestExtra,
	}); err != nil {
		return err
	}

	bar := newProgressBar(*sf.Simulations)
	dist := simulate.ItemsInDays(pool, simulate.Params{
		Mode:        mode,
		Team:        sf.Team,
		Engineers:   *sf.Engineers,
		Days:        effectiveDays,
		Simulations: *sf.Simulations,
		Workers:     *sf.Goroutines,
		Seed:        seed,
		Progress:    bar.update,
	})
	modeDescription := simulate.ModeLabel(mode, sf.Team, *sf.Engineers)

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
