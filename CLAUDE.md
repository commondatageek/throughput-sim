# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Uses [Task](https://taskfile.dev) for automation (`task` CLI required):

```bash
task build       # Compile the forecast binary to bin/
task test        # Run all Go tests
```

Manual build (mirrors `task build`):
```bash
go build -o bin/forecast ./cmd/forecast
```

Single Go module (`forecasting`, see `go.mod`) — no `go.work`. Tests exist in `cmd/forecast`, `internal/simulate`, `internal/sqlite`, and `internal/linear`; `task test` (or `go test ./...`) runs them. No linting is configured.

## Architecture

This is a Linear-only delivery-forecasting toolkit: one model (`linear.Issue`) flows into a single SQLite store, which the simulation and reporting tools query.

```
linear.Client  --Fetch-->  linear.Issue  --Upsert-->  sqlite.Store (linear.db, "issues" table)
                                                                |
                                          +---------------------+----------------------+
                                          |                                            |
                                   forecast sim                              forecast aging/cfd/count
                             (Monte Carlo forecasts)                   (cycle-time / WIP-age / CFD reports)
```

**`internal/linear`** — Defines `Issue` (the record: identifier, title, assignee, team key, team name, project, project milestone, workflow state, timestamps) and `Client`, which fetches from the Linear.app GraphQL API (`Client.Fetch`, paginated, **all** issues — no state or assignee filter; only an optional team scope and the incremental `updatedAt` watermark). `KeyList` is a `flag.Value` for comma-separated, upper-cased team keys. `toIssue` keeps every issue, mapping the GraphQL `issueNode` onto `Issue` and collapsing absent related objects (assignee, team, project, milestone, state) to empty strings; the store persists those as NULL. It uses Linear's own field names (`team.key`/`team.name` → `TeamKey`/`TeamName`, `state.type` → `StateType`, `state.name` → `StateName`, `projectMilestone.id/name` → `ProjectMilestoneID/Name`). `Client.ListTeams` lists every accessible team (`key`, `name`), used by `forecast linear teams` and `forecast linear sync -all-teams`.

**`internal/sqlite`** — The only place SQL lives. `Store` wraps a `database/sql` SQLite connection (via `modernc.org/sqlite`, pure Go, no cgo) with WAL mode and an idempotent `CREATE TABLE IF NOT EXISTS` schema (no migration framework — the store only replicates Linear's own data, so rebuilding it from scratch is trivial and cheap). `Open` creates the db file if missing (used by `linear sync`, which legitimately seeds a new database); `OpenExisting` first checks the file is present and errors clearly if not, used by every read-only command so a typo'd `-db` path fails loudly instead of silently opening an empty database. Key methods: `Upsert` (keyed on `identifier`), `LatestUpdatedAtForTeam` (per-team watermark for incremental sync), `DistinctTeamKeys` (every team key currently in the store), `CompletedBetween` (date-ranged, optionally assignee-filtered), `InProgress`, `NotCompletedCounts` (counts of non-terminal issues grouped by team, project and milestone, used by `forecast count`), `ProjectLastUpdated` (per-project max `updated_at` across **all** issues — terminal included — for the count recency filter/ordering), `CFDIssues` (all issues with their four lifecycle timestamps for CFD construction).

### Commands (`cmd/forecast`)

All commands live in the single `forecast` binary. Run `forecast` with no arguments for the full command list.

- **`forecast linear sync`** — Production ingest path. `forecast linear sync -db <path> [-teams k1,k2] [-all-teams] [-full-reload]` syncs **one team at a time**, each against its own `team_key` watermark, committing before moving to the next. With no `-teams`/`-all-teams`, it incrementally syncs every team already in the db. `-teams k1,k2` limits/extends the candidate set (new keys get a full sync); `-all-teams` expands the candidate set to every accessible Linear team (via `Client.ListTeams`); `-full-reload` ignores the watermark and full-syncs every candidate team. A brand-new/empty db requires `-teams` or `-all-teams` to seed it. Progress is logged via `log/slog`. Accepts `-config <file.yaml>` to supply flag values from a YAML file keyed by flag name; CLI flags override config values, which override built-in defaults.
- **`forecast linear teams`** — Lists accessible Linear teams (key, name) and exits.
- **`forecast sim`** — The Monte Carlo engine. Four subcommands:
  - `items` — how many items can N engineers complete in D days?
  - `days` — how many days for N engineers to complete I items?
  - `probability` — probability of completing I items in D days?
  - `backtest` — replay probability forecasts day-by-day for a project/milestone.

  Builds a `SamplePool` (per-engineer slice of daily completion counts over `[sample-start, sample-end)`) by querying `-db`, and runs `-simulations` trials by resampling, parallelized across `-goroutines` workers (each with its own seeded `*rand.Rand` — never share one across goroutines). Three sampling modes, mutually exclusive: anonymous `-engineers N` (pools all engineers' samples together), named `-team a,b,c` (each engineer draws from their own history), and `-whole-team` (sums all engineers into one daily series, ignoring individual variance). All subcommands accept `-config <file.yaml>` to supply flag values from a YAML file keyed by flag name; CLI flags override config values, which override built-in defaults.
- **`forecast count`** — Outstanding-work report. `forecast count -db <path> [-milestones] [-updated-since YYYY-MM-DD] [-teams k1,k2]` counts issues that are *not* in a terminal state (`state_type` not in `completed`/`canceled`/`duplicate` — i.e. all non-terminal issues, started or not), grouped by project. By default it prints a per-project summary table with columns for issue count and milestone count (real milestones only, i.e. excluding the `(No Milestone)` bucket); `-milestones` instead prints each project with its total followed by an indented per-milestone breakdown. `-updated-since` (default: today − 3 months) drops projects whose most-recently-updated issue predates the given date, and projects are ordered by most recently updated issue first; this "last touched" timestamp is measured across **all** the project's issues (including terminal ones, via `Store.ProjectLastUpdated`), even though the counts themselves include only non-terminal issues. `-teams` (a `linear.TeamKeyList`, comma-separated and upper-cased) filters to the given team keys; default is all teams. When `-teams` is not given and the db holds more than one team, a team name is shown alongside each project (a `TEAM` column in the summary, a `[Team]` suffix in the grouped view) and a `warning:` is logged to stderr noting that data is being blended across all teams (via `warnIfBlendingTeams` in `cmd/forecast/common.go`, shared with `aging`/`cfd`). Issues without a milestone are bucketed under `(No Milestone)` and issues without a project under `(No Project)`. Read-only (never writes the db). Accepts `-config <file.yaml>` to supply flag values from a YAML file keyed by flag name; CLI flags override config values, which override built-in defaults.
- **`forecast aging`** — WIP-age / cycle-time report. Computes the historical cycle-time distribution (`completed_at - started_at`) from completed issues in `-db`, then ranks currently in-progress issues by percentile against that distribution. Outputs `text`, `json`, or `html`. Scoped by `-teams` (default: all teams); when `-teams` is omitted and the db holds more than one team, a `warning:` is logged to stderr noting the cycle-time distribution blends all teams (via `warnIfBlendingTeams`). Accepts `-config <file.yaml>` to supply flag values from a YAML file keyed by flag name; CLI flags override config values, which override built-in defaults.
- **`forecast cfd`** — Cumulative Flow Diagram. `forecast cfd -db <path> [-teams k1,k2] [-start YYYY-MM-DD] [-end YYYY-MM-DD] [-format html|json] [-out file.html]` builds a 4-line / 3-band CFD (Created, LeftBacklog, Departed, Completed) from `canceled_at`, `started_at`, `completed_at`, and `created_at`, asserts the four CFD invariants (monotonic, nested, conserved, readable), computes flow-health stats (throughput, avg WIP per band, cycle time, Little's Law cross-check, per-band stability), and renders an interactive Plotly stacked-area chart (HTML default) or a daily-series JSON. Window defaults to today minus 3 months → today. When `-teams` is omitted and the db holds more than one team, a `warning:` is logged to stderr noting the CFD blends flow across all teams (via `warnIfBlendingTeams`). Accepts `-config <file.yaml>` to supply flag values from a YAML file keyed by flag name; CLI flags override config values, which override built-in defaults.

**`scripts/check-engineer-data.sh`** — Sanity-checks `linear.db` for a set of engineers before trusting a `forecast sim`/`forecast aging` run (completed-issue counts, distinct days with completions, zero-count days, lifetime first/last completion). Mirrors `forecast sim`'s date semantics: start inclusive, end exclusive.

### Data formats

**`linear.db`** (SQLite, the only data store) — single `issues` table, primary key `identifier`. Schema defined inline in `internal/sqlite/store.go` (`schema` constant). Columns are faithful transliterations of Linear's own field names (e.g. `team_key`/`team_name` from `team.key`/`team.name`, `state_type`/`state_name` from `state.type`/`state.name`, `project_milestone_id`/`project_milestone_name` from `projectMilestone`). The genuinely-optional columns (`assignee`, `project_id`, `project_name`, `project_milestone_id`, `project_milestone_name`) are nullable and stored as NULL when absent (via `nullString` in `internal/sqlite`); always-present columns (`title`, `team_key`, `team_name`, `state_type`, `state_name`) are `NOT NULL DEFAULT ''`. Reads coalesce NULL back to `""`, so consumers still see plain strings. `canceled_at` is a nullable DATETIME column added for CFD support; populated by re-syncing after any schema reset.

**`exclusions.json`** (optional input to `forecast sim`, e.g. for holidays):
```json
{
  "global": ["2024-12-25"],
  "engineers": {"alice": ["2024-06-17"]}
}
```

### Config files (`-config`)

Every subcommand accepts `-config <file.yaml>`, applied via `util.ApplyConfig` (`internal/util/config.go`) immediately after `fs.Parse`. Precedence is **CLI flag > config file > built-in default**. Rules:

- **Keys equal flag names**, exactly as passed on the command line (e.g. `-sample-end` → `sample-end`, `-random-seed` → `random-seed`).
- **List flags** (`-teams`, `-team`, `-typical-engineers`, `-percentile`, `-items`) take a YAML sequence, joined into the same comma-separated string the flag itself accepts: `teams: [ENG, DATA]` behaves identically to `-teams ENG,DATA`. A plain string (`teams: "ENG,DATA"`) works too.
- **Presence-sensitive flags behave as if passed on the CLI.** Config values are applied via `fs.Set`, so `sample-end` or `random-seed` set only in a config file still counts as "explicitly set" for `resolveEndDate`/`resolveSeed` — e.g. a `random-seed: 42` in config pins the seed exactly like `-random-seed 42` would, rather than falling back to the time-based default.
- The `config` key itself is reserved/ignored inside the file (prevents self-reference).
- One config file's keys are shared by exactly one command's `FlagSet` — there's no per-command sectioning (a `sim items` config and a `count` config are separate files); see Stage 4's non-goal on a shared multi-command file.

Example config for `forecast sim items` (`sim-items.yaml`):
```yaml
db: linear.db
engineers: 4
days: 30
sample-start: "2025-01-01"
sample-end: "2025-07-01"
random-seed: 42
percentile: [5, 25, 50, 75, 95]
team: [alice, bob]
```
```bash
forecast sim items -config sim-items.yaml            # uses every value above
forecast sim items -config sim-items.yaml -days 60   # CLI -days wins over the file's 30
```

Example for `forecast linear sync` (`sync.yaml`):
```yaml
db: linear.db
teams: [ENG, DATA]
full-reload: true
```

Example for `forecast count` (`count.yaml`):
```yaml
db: linear.db
milestones: true
updated-since: "2025-04-01"
teams: [ENG, DESIGN]
```

Example for `forecast aging` (`aging.yaml`):
```yaml
db: linear.db
format: html
sample-start: "2025-01-01"
min-cycle-time: 1h
```

Example for `forecast cfd` (`cfd.yaml`):
```yaml
db: linear.db
start: "2025-04-01"
end: "2025-07-01"
format: json
```

### Conventions worth knowing

- `-sample-end` semantics: if explicitly set, it's a calendar date (midnight, that day excluded). If omitted, it defaults to *now* so today's already-completed work counts (see `resolveEndDate` in `cmd/forecast/common.go`).
- Random seeding: `-random-seed` is time-based (non-deterministic) unless explicitly passed, via the `isFlagSet` flag-presence pattern in `cmd/forecast/common.go`.

## On-call modeling

`ONCALL_MODELING.md` documents a planned (not yet implemented) feature to model on-call rotations. Two design options are discussed: a `-oncall-fraction` flag vs. separate sample pools for on-call vs. normal days.
