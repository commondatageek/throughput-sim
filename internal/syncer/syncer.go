// Package syncer implements the Linear ingest helper: it syncs one team at a
// time from the Linear API into a SQLite store, committing each team's issues
// before moving to the next so a failure partway through leaves already-synced
// teams resumable from their own watermark.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
)

// Options controls which teams are synced and how. Every field mirrors a
// `forecast linear sync` flag (and thus a `-config` YAML key of the same
// name, lower-cased with hyphens, e.g. `full-reload`).
type Options struct {
	// Teams is the `-teams` flag: candidate team keys to sync, extending
	// whatever teams already exist in the store. Mutually exclusive with AllTeams.
	Teams linear.TeamKeyList
	// AllTeams is the `-all-teams` flag: expand the candidate set to every
	// team the Linear API token can access. Mutually exclusive with Teams.
	AllTeams bool
	// FullReload is the `-full-reload` flag: ignore each team's watermark and
	// re-sync its full issue history instead of an incremental sync.
	FullReload bool
}

// client is the subset of *linear.Client that Run needs. It exists as a test
// seam local to this package (not the source abstraction the original design
// deliberately avoided) so Run can be exercised against a stub instead of the
// real Linear API. *linear.Client satisfies it.
type client interface {
	Fetch(ctx context.Context, updatedSince time.Time, teamKeys []string) ([]linear.Issue, error)
	ListTeams(ctx context.Context) ([]linear.Team, error)
}

// Run syncs issues from the Linear API into store according to opts.
// It uses slog.Default() for progress logging; configure the default logger
// before calling if you want structured output.
func Run(ctx context.Context, client client, store *sqlite.Store, opts Options) error {
	if opts.AllTeams && len(opts.Teams) > 0 {
		return fmt.Errorf("-teams and -all-teams are mutually exclusive")
	}

	existing, err := store.DistinctTeamKeys(ctx)
	if err != nil {
		return fmt.Errorf("distinct team keys: %w", err)
	}
	existingSet := make(map[string]bool, len(existing))
	for _, k := range existing {
		existingSet[k] = true
	}

	candidates := opts.Teams
	if opts.AllTeams {
		teamNodes, err := client.ListTeams(ctx)
		if err != nil {
			return fmt.Errorf("list teams: %w", err)
		}
		candidates = make(linear.TeamKeyList, 0, len(teamNodes))
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
		full := !exists || opts.FullReload

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
