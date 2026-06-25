# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Uses [Task](https://taskfile.dev) for automation (`task` CLI required):

```bash
task build       # Compile all Go binaries to bin/
task fetch       # Sync all issues from Linear into linear.db (requires LINEAR_API_KEY)
task test        # Run all Go tests
```

Manual builds (mirrors `task build`):
```bash
go build -C cmd/aging-report -o ../../bin/aging-report .
go build -C cmd/cfd -o ../../bin/cfd .
go build -C cmd/count-issues -o ../../bin/count-issues .
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

**`internal/linear`** — Defines `Issue` (the record: identifier, title, assignee, team key, team name, project, project milestone, workflow state, timestamps) and `Client`, which fetches from the Linear.app GraphQL API (`Client.Fetch`, paginated, **all** issues — no state or assignee filter; only an optional team scope and the incremental `updatedAt` watermark). `KeyList` is a `flag.Value` for comma-separated, upper-cased team keys. `toIssue` keeps every issue, mapping the GraphQL `issueNode` onto `Issue` and collapsing absent related objects (assignee, team, project, milestone, state) to empty strings; the store persists those as NULL. It uses Linear's own field names (`team.key`/`team.name` → `TeamKey`/`TeamName`, `state.type` → `StateType`, `state.name` → `StateName`, `projectMilestone.id/name` → `ProjectMilestoneID/Name`). `Client.ListTeams` lists every accessible team (`key`, `name`), used by `cmd/list-teams` and `sync -all-teams`.

**`internal/sqlite`** — The only place SQL lives. `Store` wraps a `database/sql` SQLite connection (via `modernc.org/sqlite`, pure Go, no cgo) with WAL mode and goose migrations embedded from `migrations/*.sql`. Key methods: `Upsert` (keyed on `identifier`), `LatestUpdatedAtForTeam` (per-team watermark for incremental sync), `DistinctTeamKeys` (every team key currently in the store), `CompletedBetween` (date-ranged, optionally assignee-filtered), `InProgress`, `NotCompletedCounts` (counts of non-terminal issues grouped by team, project and milestone, used by `cmd/count-issues`), `ProjectLastUpdated` (per-project max `updated_at` across **all** issues — terminal included — for the count-issues recency filter/ordering), `CFDIssues` (all issues with their four lifecycle timestamps for CFD construction).

### Commands (`cmd/`)

- **`sync`** — Production path. Database-centric: `bin/sync [-teams k1,k2] [-all-teams] [-full-reload] <db-path>` takes the db file as a required positional argument *after* any flags (standard `flag` package ordering — flags must precede `flag.Arg(0)`) and syncs **one team at a time**, each against its own `team_key` watermark, committing before moving to the next. With no `-teams`/`-all-teams`, it incrementally syncs every team already in the db. `-teams k1,k2` limits/extends the candidate set (new keys get a full sync); `-all-teams` expands the candidate set to every accessible Linear team (via `Client.ListTeams`); `-full-reload` ignores the watermark and full-syncs every candidate team. A brand-new/empty db requires `-teams` or `-all-teams` to seed it. Progress is logged via `log/slog`.
- **`list-teams`** — Lists accessible Linear teams (key, name) and exits.
- **`sim`** — The Monte Carlo engine. Three subcommands:
  - `items` — how many items can N engineers complete in D days?
  - `days` — how many days for N engineers to complete I items?
  - `probability` — probability of completing I items in D days?

  Builds a `SamplePool` (per-engineer slice of daily completion counts over `[sample-start, sample-end)`) by querying `-db` (default `linear.db`), and runs `-simulations` trials by resampling, parallelized across `-goroutines` workers (each with its own seeded `*rand.Rand` — never share one across goroutines). Three sampling modes, mutually exclusive: anonymous `-engineers N` (pools all engineers' samples together), named `-team a,b,c` (each engineer draws from their own history), and `-whole-team` (sums all engineers into one daily series, ignoring individual variance).
- **`count-issues`** — Outstanding-work report. `bin/count-issues [-milestones] [-updated-since YYYY-MM-DD] [-teams k1,k2] <db-path>` takes the db file as the single required positional argument and counts issues that are *not* in a terminal state (`state_type` not in `completed`/`canceled`/`duplicate` — i.e. all non-terminal issues, started or not), grouped by project. By default it prints a per-project summary table with columns for issue count and milestone count (real milestones only, i.e. excluding the `(No Milestone)` bucket); `-milestones` instead prints each project with its total followed by an indented per-milestone breakdown. `-updated-since` (default: today − 3 months) drops projects whose most-recently-updated issue predates the given date, and projects are ordered by most recently updated issue first; this "last touched" timestamp is measured across **all** the project's issues (including terminal ones, via `Store.ProjectLastUpdated`), even though the counts themselves include only non-terminal issues. `-teams` (a `linear.KeyList`, comma-separated and upper-cased) filters to the given team keys; default is all teams. When `-teams` is not given and the db holds more than one team, a team name is shown alongside each project (a `TEAM` column in the summary, a `[Team]` suffix in the grouped view). Issues without a milestone are bucketed under `(No Milestone)` and issues without a project under `(No Project)`. Read-only (never writes the db).
- **`aging-report`** — WIP-age / cycle-time report. Computes the historical cycle-time distribution (`completed_at - started_at`) from completed issues in `-db` (default `linear.db`), then ranks currently in-progress issues by percentile against that distribution. Outputs `text`, `json`, or `html`.
- **`cfd`** — Cumulative Flow Diagram. `bin/cfd [-db linear.db] [-teams k1,k2] [-start YYYY-MM-DD] [-end YYYY-MM-DD] [-format html|json] [-out file.html]` builds a 4-line / 3-band CFD (Created, LeftBacklog, Departed, Completed) from `canceled_at`, `started_at`, `completed_at`, and `created_at`, asserts the four CFD invariants (monotonic, nested, conserved, readable), computes flow-health stats (throughput, avg WIP per band, cycle time, Little's Law cross-check, per-band stability), and renders an interactive Plotly stacked-area chart (HTML default) or a daily-series JSON. Window defaults to last 90 days → today.

**`scripts/check-engineer-data.sh`** — Sanity-checks `linear.db` for a set of engineers before trusting a `sim`/`aging-report` run (completed-issue counts, distinct days with completions, zero-count days, lifetime first/last completion). Mirrors `sim`'s date semantics: start inclusive, end exclusive.

### Data formats

**`linear.db`** (SQLite, the only data store) — single `issues` table, primary key `identifier`. Schema in `internal/sqlite/migrations/00001_create_issues.sql`. Columns are faithful transliterations of Linear's own field names (e.g. `team_key`/`team_name` from `team.key`/`team.name`, `state_type`/`state_name` from `state.type`/`state.name`, `project_milestone_id`/`project_milestone_name` from `projectMilestone`). The genuinely-optional columns (`assignee`, `project_id`, `project_name`, `project_milestone_id`, `project_milestone_name`) are nullable and stored as NULL when absent (via `nullString` in `internal/sqlite`); always-present columns (`title`, `team_key`, `team_name`, `state_type`, `state_name`) are `NOT NULL DEFAULT ''`. Reads coalesce NULL back to `""`, so consumers still see plain strings. `canceled_at` is a nullable DATETIME column added for CFD support; populated by re-syncing after any schema reset.

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
