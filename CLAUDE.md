# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Uses [Task](https://taskfile.dev) for automation (`task` CLI required):

```bash
task build       # Compile all Go binaries to bin/
task fetch       # Sync completed/in-progress issues from Linear into linear.db (requires LINEAR_API_KEY)
task test        # Run all Go tests
```

Manual builds (mirrors `task build`):
```bash
go build -C cmd/aging-report -o ../../bin/aging-report .
go build -C cmd/sim -o ../../bin/sim .
go build -C cmd/sync -o ../../bin/sync .
```

Single Go module (`forecasting`, see `go.mod`) — no `go.work`. Tests exist in `cmd/sim`, `internal/sqlite`, and `internal/linear`; `task test` (or `go test ./...`) runs them. No linting is configured.

## Architecture

This is a Linear-only throughput-forecasting toolkit: one model (`linear.Issue`) flows into a single SQLite store, which the simulation and reporting tools query.

```
linear.Client  --Fetch-->  linear.Issue  --Upsert-->  sqlite.Store (linear.db, "issues" table)
                                                                |
                                          +---------------------+----------------------+
                                          |                                            |
                                     cmd/sim                                  cmd/aging-report
                              (Monte Carlo forecasts)                    (cycle-time / WIP-age report)
```

**`internal/linear`** — Defines `Issue` (the record: identifier, title, assignee, team, project, project milestone, workflow state, timestamps) and `Client`, which fetches from the Linear.app GraphQL API (`Client.Fetch`, paginated, filters to `completed`/`started` issues with a non-null assignee). `KeyList` is a `flag.Value` for comma-separated, upper-cased team keys. In-progress issues without a `startedAt` are dropped (can't be used for aging). `toIssue` maps the GraphQL `issueNode` onto `Issue`, using Linear's own field names (`state.type` → `StateType`, `state.name` → `StateName`, `projectMilestone.id/name` → `ProjectMilestoneID/Name`).

**`internal/sqlite`** — The only place SQL lives. `Store` wraps a `database/sql` SQLite connection (via `modernc.org/sqlite`, pure Go, no cgo) with WAL mode and goose migrations embedded from `migrations/*.sql`. Key methods: `Upsert` (keyed on `identifier`), `LatestUpdatedAt` (watermark for incremental sync), `CompletedBetween` (date-ranged, optionally assignee-filtered), `InProgress`.

### Commands (`cmd/`)

- **`sync`** — Production path. Fetches from Linear and upserts into `linear.db`. `-sync-all` forces a full reload ignoring the watermark; `-all-teams` vs `-teams` selects scope; `-list-teams` lists accessible teams and exits.
- **`sim`** — The Monte Carlo engine. Three subcommands:
  - `items` — how many items can N engineers complete in D days?
  - `days` — how many days for N engineers to complete I items?
  - `probability` — probability of completing I items in D days?

  Builds a `SamplePool` (per-engineer slice of daily completion counts over `[sample-start, sample-end)`) by querying `-db` (default `linear.db`), and runs `-simulations` trials by resampling, parallelized across `-goroutines` workers (each with its own seeded `*rand.Rand` — never share one across goroutines). Three sampling modes, mutually exclusive: anonymous `-engineers N` (pools all engineers' samples together), named `-team a,b,c` (each engineer draws from their own history), and `-whole-team` (sums all engineers into one daily series, ignoring individual variance).
- **`aging-report`** — WIP-age / cycle-time report. Computes the historical cycle-time distribution (`completed_at - started_at`) from completed issues in `-db` (default `linear.db`), then ranks currently in-progress issues by percentile against that distribution. Outputs `text`, `json`, or `html`.

**`scripts/check-engineer-data.sh`** — Sanity-checks `linear.db` for a set of engineers before trusting a `sim`/`aging-report` run (completed-issue counts, distinct days with completions, zero-count days, lifetime first/last completion). Mirrors `sim`'s date semantics: start inclusive, end exclusive.

### Data formats

**`linear.db`** (SQLite, the only data store) — single `issues` table, primary key `identifier`. Schema in `internal/sqlite/migrations/00001_create_issues.sql`. Columns are faithful transliterations of Linear's own field names (e.g. `state_type`/`state_name` from `state.type`/`state.name`, `project_milestone_id`/`project_milestone_name` from `projectMilestone`).

**`exclusions.json`** (optional input to `sim`, e.g. for holidays):
```json
{
  "global": ["2024-12-25"],
  "engineers": {"alice": ["2024-06-17"]}
}
```

### Conventions worth knowing

- `-sample-end` semantics: if explicitly set, it's a calendar date (midnight, that day excluded). If omitted, it defaults to *now* so today's already-completed work counts (see `resolveEndDate`/`daysBetween` in `cmd/sim/main.go`).
- Random seeding: `-random-seed` is time-based (non-deterministic) unless explicitly passed, via the `isFlagSet` flag-presence pattern in `cmd/sim/main.go`.

## On-call modeling

`ONCALL_MODELING.md` documents a planned (not yet implemented) feature to model on-call rotations. Two design options are discussed: a `-oncall-fraction` flag vs. separate sample pools for on-call vs. normal days.
