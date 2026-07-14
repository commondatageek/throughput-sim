package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"forecasting/internal/counts"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

func cmdCount(args []string) error {
	defaultSince := time.Now().AddDate(0, -3, 0).Format("2006-01-02")

	cmd := flag.NewFlagSet("count", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	milestones := cmd.Bool("milestones", false, "add a per-milestone breakdown under each project")
	updatedSince := cmd.String("updated-since", defaultSince, "only include projects with an issue updated on/after this date (YYYY-MM-DD)")
	teams := addTeamsFlag(cmd, "comma-separated team keys to filter by (e.g. ENG,DESIGN); default: all teams")
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	since, err := util.ParseDate(*updatedSince)
	if err != nil {
		return fmt.Errorf("invalid -updated-since %q (want YYYY-MM-DD): %w", *updatedSince, err)
	}

	opts := counts.Options{Teams: *teams, Since: since}

	projects, total, multiTeam, err := loadCountProjects(*dbFile, opts)
	if err != nil {
		return err
	}

	showTeams := len(opts.Teams) == 0 && multiTeam

	if *milestones {
		return counts.RenderGrouped(os.Stdout, projects, total, showTeams)
	}
	return counts.RenderSummary(os.Stdout, projects, total, showTeams)
}

// loadCountProjects reads the not-completed issue counts from the store and
// returns the folded project list. It also reports whether the database holds
// more than one team.
func loadCountProjects(dbPath string, opts counts.Options) ([]counts.Project, int, bool, error) {
	store, err := sqlite.OpenExisting(dbPath)
	if err != nil {
		return nil, 0, false, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	ctx := context.Background()

	allTeams, err := store.DistinctTeamKeys(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	multiTeam := len(allTeams) > 1

	if msg := blendingTeamsWarning(opts.Teams, allTeams); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}

	countRows, err := store.NotCompletedCounts(ctx, opts.Teams)
	if err != nil {
		return nil, 0, false, err
	}
	if len(countRows) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no outstanding (non-terminal) issues found for the given filters")
	}

	activity, err := store.ProjectLastUpdated(ctx, opts.Teams)
	if err != nil {
		return nil, 0, false, err
	}

	projects, total := counts.Compute(countRows, activity, opts.Since)
	return projects, total, multiTeam, nil
}
