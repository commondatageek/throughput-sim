package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"forecasting/internal/linear"
)

// outputIssue preserves the existing issues.json wire format.
type outputIssue struct {
	Engineer    string `json:"engineer"`
	Team        string `json:"team"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	ProjectName string `json:"project_name"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	Status      string `json:"status"`
}

func main() {
	var teams linear.KeyList
	flag.Var(&teams, "teams", "comma-separated list of Linear team keys (e.g. ENG,DESIGN); required unless -all-teams")
	allTeams := flag.Bool("all-teams", false, "fetch issues for all accessible teams; mutually exclusive with -teams")
	listTeamsFlag := flag.Bool("list-teams", false, "list accessible teams and their keys, then exit")
	flag.Parse()

	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: LINEAR_API_KEY environment variable is not set")
		os.Exit(1)
	}

	src := linear.New(apiKey, []string(teams))

	if *listTeamsFlag {
		if err := src.ListTeams(context.Background(), os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *allTeams && len(teams) > 0 {
		fmt.Fprintln(os.Stderr, "error: -teams and -all-teams are mutually exclusive")
		os.Exit(1)
	}
	if !*allTeams && len(teams) == 0 {
		fmt.Fprintln(os.Stderr, "error: must specify -teams (comma-separated team keys) or -all-teams")
		os.Exit(1)
	}

	if *allTeams {
		fmt.Fprintln(os.Stderr, "fetching completed and in-progress issues for all accessible teams")
	} else {
		fmt.Fprintf(os.Stderr, "filtering to teams: %s\n", strings.Join(teams, ", "))
	}

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	for _, it := range items {
		startedAt := ""
		if !it.StartedAt.IsZero() {
			startedAt = it.StartedAt.UTC().Format(time.RFC3339)
		}
		completedAt := ""
		if !it.CompletedAt.IsZero() {
			completedAt = it.CompletedAt.UTC().Format(time.RFC3339)
		}
		out := outputIssue{
			Engineer:    it.Assignee,
			Team:        it.Team,
			Identifier:  it.Identifier,
			Title:       it.Title,
			ProjectName: it.ProjectName,
			StartedAt:   startedAt,
			CompletedAt: completedAt,
			Status:      it.Status,
		}
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "error encoding output: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "done. total issues: %d\n", len(items))
}
