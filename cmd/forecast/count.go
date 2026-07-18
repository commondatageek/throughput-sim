package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/commondatageek/delivery-forecast/counts"
	"github.com/commondatageek/delivery-forecast/internal/logx"
	"github.com/commondatageek/delivery-forecast/internal/sqlite"
	"github.com/commondatageek/delivery-forecast/internal/util"
)

// toProjectMilestoneCounts converts sqlite.ProjectMilestoneCount records to
// counts.ProjectMilestoneCount.
func toProjectMilestoneCounts(rows []sqlite.ProjectMilestoneCount) []counts.ProjectMilestoneCount {
	out := make([]counts.ProjectMilestoneCount, len(rows))
	for i, r := range rows {
		out[i] = counts.ProjectMilestoneCount{
			TeamKey:       r.TeamKey,
			TeamName:      r.TeamName,
			ProjectName:   r.ProjectName,
			MilestoneName: r.MilestoneName,
			Count:         r.Count,
		}
	}
	return out
}

// toProjectActivity converts sqlite.ProjectActivity records to
// counts.ProjectActivity.
func toProjectActivity(rows []sqlite.ProjectActivity) []counts.ProjectActivity {
	out := make([]counts.ProjectActivity, len(rows))
	for i, r := range rows {
		out[i] = counts.ProjectActivity{
			TeamKey:     r.TeamKey,
			TeamName:    r.TeamName,
			ProjectName: r.ProjectName,
			LastUpdated: r.LastUpdated,
		}
	}
	return out
}

func cmdCount(args []string) error {
	defaultSince := time.Now().AddDate(0, -3, 0).Format("2006-01-02")

	cmd := flag.NewFlagSet("count", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	milestones := cmd.Bool("milestones", false, "add a per-milestone breakdown under each project")
	updatedSince := cmd.String("updated-since", defaultSince, `only include projects with an issue updated on/after this date (YYYY-MM-DD; or: now, yesterday, today, tomorrow, "-3 months")`)
	teams := addTeamsFlag(cmd, "comma-separated team keys to filter by (e.g. ENG,DESIGN); default: all teams")
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	since, err := util.ParseFlexibleDate(*updatedSince, time.Now())
	if err != nil {
		return fmt.Errorf("invalid -updated-since %q: %w", *updatedSince, err)
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
		logx.Warnf("%s", msg)
	}

	countRows, err := store.NotCompletedCounts(ctx, opts.Teams)
	if err != nil {
		return nil, 0, false, err
	}
	if len(countRows) == 0 {
		logx.Warnf("no outstanding (non-terminal) issues found for the given filters")
	}

	activity, err := store.ProjectLastUpdated(ctx, opts.Teams)
	if err != nil {
		return nil, 0, false, err
	}

	projects, total := counts.Compute(toProjectMilestoneCounts(countRows), toProjectActivity(activity), opts.Since)
	return projects, total, multiTeam, nil
}
