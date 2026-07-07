// Command count-issues reports how many issues are not yet completed, broken
// down by project (and optionally milestone). It reads a single SQLite database
// (the required positional argument) and never modifies it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"forecasting/internal/counts"
	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

const dateLayout = "2006-01-02"

func main() {
	defaultSince := time.Now().AddDate(0, -3, 0).Format(dateLayout)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [-milestones] [-updated-since YYYY-MM-DD] [-teams k1,k2] <db-path>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Report the number of not-completed issues, grouped by project (and optionally milestone).")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		flag.PrintDefaults()
	}
	milestones := flag.Bool("milestones", false, "Add a per-milestone breakdown under each project")
	updatedSince := flag.String("updated-since", defaultSince, "Only include projects with an issue updated on/after this date (YYYY-MM-DD)")
	var teams linear.KeyList
	flag.Var(&teams, "teams", "Comma-separated team keys to filter by (e.g. ENG,DESIGN); default: all teams")
	configFile := flag.String("config", "", "path to a YAML config file supplying flag values (CLI flags override)")
	flag.Parse()

	if err := util.ApplyConfig(flag.CommandLine, *configFile); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	since, err := time.Parse(dateLayout, *updatedSince)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid -updated-since %q (want YYYY-MM-DD): %v\n", *updatedSince, err)
		os.Exit(1)
	}

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "error: exactly one argument (the database file) is required")
		flag.Usage()
		os.Exit(1)
	}
	dbPath := flag.Arg(0)

	projects, total, multiTeam, err := loadProjects(dbPath, teams, since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	showTeams := len(teams) == 0 && multiTeam

	if *milestones {
		if err := counts.RenderGrouped(os.Stdout, projects, total, showTeams); err != nil {
			fmt.Fprintf(os.Stderr, "error: render: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := counts.RenderSummary(os.Stdout, projects, total, showTeams); err != nil {
			fmt.Fprintf(os.Stderr, "error: render: %v\n", err)
			os.Exit(1)
		}
	}
}

// loadProjects reads the not-completed issue counts from the store and returns
// the folded project list. It also reports whether the database holds more than
// one team.
func loadProjects(dbPath string, teamKeys []string, since time.Time) ([]counts.Project, int, bool, error) {
	store, err := sqlite.Open(dbPath)
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

	countRows, err := store.NotCompletedCounts(ctx, teamKeys)
	if err != nil {
		return nil, 0, false, err
	}

	activity, err := store.ProjectLastUpdated(ctx, teamKeys)
	if err != nil {
		return nil, 0, false, err
	}

	projects, total := counts.Compute(countRows, activity, since)
	return projects, total, multiTeam, nil
}
