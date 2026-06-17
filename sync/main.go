package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
	internalsync "forecasting/internal/sync"
)

func main() {
	source := flag.String("source", "linear", "data source to sync (currently: linear)")
	teams := flag.String("teams", "", "comma-separated team keys, e.g. ENG,DESIGN (source=linear only)")
	db := flag.String("db", "items.db", "path to SQLite database file")
	flag.Parse()

	if err := run(context.Background(), *source, *teams, *db); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, source, teamsFlag, dbPath string) error {
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	switch source {
	case "linear":
		return syncLinear(ctx, teamsFlag, store)
	default:
		return fmt.Errorf("unknown source %q; supported: linear", source)
	}
}

func syncLinear(ctx context.Context, teamsFlag string, store *sqlite.Store) error {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("LINEAR_API_KEY environment variable is not set")
	}

	var teamKeys []string
	for _, t := range strings.Split(teamsFlag, ",") {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t != "" {
			teamKeys = append(teamKeys, t)
		}
	}

	if len(teamKeys) == 0 {
		fmt.Fprintln(os.Stderr, "fetching completed and in-progress issues for all accessible teams")
	} else {
		fmt.Fprintf(os.Stderr, "filtering to teams: %s\n", strings.Join(teamKeys, ", "))
	}

	src := linear.New(apiKey, teamKeys)

	n, err := internalsync.Sync(ctx, src, store)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done. upserted %d items.\n", n)
	return nil
}
