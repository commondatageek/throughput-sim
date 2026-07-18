package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/commondatageek/delivery-forecast/internal/aging"
	"github.com/commondatageek/delivery-forecast/internal/logx"
	"github.com/commondatageek/delivery-forecast/internal/sqlite"
	"github.com/commondatageek/delivery-forecast/internal/util"
)

func cmdAging(args []string) error {
	cmd := flag.NewFlagSet("aging", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	sampleStartStr := cmd.String("sample-start", "-3 months", `start of completed-issue window (YYYY-MM-DD; or: yesterday, today, tomorrow, "-3 months")`)
	sampleEndStr := cmd.String("sample-end", "today", `end of completed-issue window (YYYY-MM-DD; or: now, yesterday, today, tomorrow, "-3 months")`)
	format := cmd.String("format", "text", "output format: text, json, html")
	minCycleTimeStr := cmd.String("min-cycle-time", "", "exclude completed issues with cycle time below this duration (e.g. 5m, 1h, 1d)")
	showCompleted := cmd.Bool("show-completed", false, "text/html: also list the completed issues that make up the percentile distribution sample")
	teams := addTeamsFlag(cmd, "comma-separated team keys to filter by (e.g. DATA,PLT); default: all teams")
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	var minCycleTime time.Duration
	if *minCycleTimeStr != "" {
		d, err := util.ParseFlexibleDuration(*minCycleTimeStr)
		if err != nil {
			return fmt.Errorf("invalid -min-cycle-time %q: %w", *minCycleTimeStr, err)
		}
		minCycleTime = d
	}

	now := time.Now()
	today := util.LocalDay(now)

	sampleEnd, err := util.ParseFlexibleDate(*sampleEndStr, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-end %q: %w", *sampleEndStr, err)
	}

	sampleStart, err := util.ParseFlexibleStartDate(*sampleStartStr, now)
	if err != nil {
		return fmt.Errorf("invalid -sample-start %q: %w", *sampleStartStr, err)
	}

	if !sampleStart.Before(sampleEnd) {
		return fmt.Errorf("-sample-start must be before -sample-end")
	}

	opts := aging.Options{Teams: *teams, SampleStart: sampleStart, SampleEnd: sampleEnd, MinCycleTime: minCycleTime}

	store, err := sqlite.OpenExisting(*dbFile)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	ctx := context.Background()

	if err := warnIfBlendingTeams(ctx, store, opts.Teams); err != nil {
		return err
	}

	completed, err := store.CompletedBetween(ctx, opts.SampleStart, opts.SampleEnd, nil, opts.Teams)
	if err != nil {
		return fmt.Errorf("query completed: %w", err)
	}

	active, err := store.InProgress(ctx, opts.Teams)
	if err != nil {
		return fmt.Errorf("query in-progress: %w", err)
	}

	cycleTimes := aging.CycleTimes(completed, opts.MinCycleTime)
	sort.Float64s(cycleTimes)

	inProgress := aging.InProgressItems(active, today)
	aging.RankItems(inProgress, cycleTimes)

	sort.Slice(inProgress, func(i, j int) bool {
		return inProgress[i].AgeDays > inProgress[j].AgeDays
	})

	var completedItems []aging.Item
	if *showCompleted {
		completedItems = aging.CompletedItems(completed, opts.MinCycleTime)
		aging.RankItems(completedItems, cycleTimes)

		sort.Slice(completedItems, func(i, j int) bool {
			return completedItems[i].AgeDays > completedItems[j].AgeDays
		})
	}

	p85 := util.PercentileValue(cycleTimes, 85)

	if len(cycleTimes) == 0 {
		logx.Warnf("no completed issues found in the sample window; percentiles will be 0")
	}

	switch *format {
	case "text":
		return aging.RenderText(os.Stdout, inProgress, completedItems, *showCompleted, cycleTimes, p85, opts.SampleStart, opts.SampleEnd)
	case "json":
		return aging.RenderJSON(os.Stdout, inProgress)
	case "html":
		return aging.RenderHTML(os.Stdout, inProgress, completedItems, *showCompleted, p85, opts.SampleStart, opts.SampleEnd, len(cycleTimes))
	default:
		return fmt.Errorf("unknown -format %q (use text, json, or html)", *format)
	}
}
