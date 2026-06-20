package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
)

func main() {
	var teams linear.KeyList
	flag.Var(&teams, "teams", "comma-separated team keys, e.g. ENG,DESIGN; limits the candidate team set")
	allTeams := flag.Bool("all-teams", false, "expand the candidate team set to every accessible Linear team; mutually exclusive with -teams")
	fullReload := flag.Bool("full-reload", false, "ignore each team's stored watermark and do a full reload")

	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	dbPath := flag.Arg(0)
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "error: usage: sync <db-path> [-teams k1,k2] [-all-teams] [-full-reload]")
		os.Exit(1)
	}

	apiKey, err := linear.GetAPIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	client := linear.New(apiKey)
	ctx := context.Background()

	if err := run(ctx, client, dbPath, teams, *allTeams, *fullReload); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// run syncs one team at a time against dbPath, committing each team's issues
// before moving to the next so a failure partway through (e.g. rate limiting)
// leaves already-synced teams resumable from their own watermark next run.
func run(ctx context.Context, client *linear.Client, dbPath string, teams linear.KeyList, allTeams, fullReload bool) error {
	if allTeams && len(teams) > 0 {
		return fmt.Errorf("-teams and -all-teams are mutually exclusive")
	}

	store, err := sqlite.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	existing, err := store.DistinctTeamKeys(ctx)
	if err != nil {
		return fmt.Errorf("distinct team keys: %w", err)
	}
	existingSet := make(map[string]bool, len(existing))
	for _, k := range existing {
		existingSet[k] = true
	}

	candidates := teams
	if allTeams {
		teamNodes, err := client.ListTeams(ctx)
		if err != nil {
			return fmt.Errorf("list teams: %w", err)
		}
		candidates = make(linear.KeyList, 0, len(teamNodes))
		for _, t := range teamNodes {
			candidates = append(candidates, t.Key)
		}
	}
	if len(candidates) == 0 {
		if len(existing) == 0 {
			return fmt.Errorf("database has no issues yet; specify -teams to seed it")
		}
		candidates = existing
	}

	for _, key := range candidates {
		exists := existingSet[key]
		full := !exists || fullReload

		var since time.Time
		if full {
			if exists {
				slog.Info("full sync (full reload)", "team", key)
			} else {
				slog.Info("full sync (new team)", "team", key)
			}
		} else {
			since, err = store.LatestUpdatedAtForTeam(ctx, key)
			if err != nil {
				return fmt.Errorf("watermark for %s: %w", key, err)
			}
			slog.Info("incremental sync", "team", key, "since", since)
		}

		issues, err := client.Fetch(ctx, since, []string{key})
		if err != nil {
			return fmt.Errorf("fetch %s: %w", key, err)
		}
		if len(issues) > 0 {
			if err := store.Upsert(ctx, issues...); err != nil {
				return fmt.Errorf("upsert %s: %w", key, err)
			}
		}
		slog.Info("upserted", "team", key, "count", len(issues))
	}

	return nil
}
