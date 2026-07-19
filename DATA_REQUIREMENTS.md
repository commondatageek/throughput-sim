# Data requirements

`forecast`'s SQLite `issues` table (schema in [`internal/sqlite/store.go`](internal/sqlite/store.go)) is the
interchange format every report/simulation command reads from. Column names
are Linear-derived — they mirror Linear's own GraphQL field names — but
nothing downstream actually requires Linear as the source: `linear sync` is
one way to populate the table, not the only one. If you populate the same
table from another system (Jira, GitHub Issues, a spreadsheet, ...), every
`forecast` command works unmodified. Timestamps are stored as SQLite
`DATETIME`; once loaded into Go, the analysis packages bucket them to local
calendar days (see each public package's doc comment, e.g. `go doc
./simulate`, for the exact rule — it differs slightly per package).

If you're not using the `forecast` CLI or the `issues` table at all — just
calling the `simulate`/`aging`/`cfd`/`counts` packages directly as a Go
library — skip to [Using this as a library](#using-this-as-a-library) below;
the table/column requirements are about what `cmd/forecast` itself needs,
not about the packages' Go API, which takes plain structs.

## Per-command requirements

One row per query a command issues (a command may issue more than one).
"Required" means the row is silently dropped or excluded if the field is
missing; "filter semantics" describes what's matched or excluded; "optional /
display-only" fields are read but don't affect which rows are included.

| Command | Required fields | Filter semantics | Optional / display-only fields |
|---|---|---|---|
| `sim items` / `sim days` / `sim probability` (sample pool) | `assignee` (non-empty), `completed_at` | `state_type = 'completed'`. No team filtering — `sim` has no `-teams` flag; every team in the db is always pooled together. | `identifier`, `title`, `team_name`, `project_name`, `started_at`, `updated_at` (feed `-manifest`'s issue dump only) |
| `sim backtest` (sample pool) | same as above | same as above | same as above |
| `sim backtest` (backtested issue set) | `created_at`, `started_at`, `completed_at` | `project_name` exact match (required); `project_milestone_name` exact match (optional); `state_type NOT IN ('canceled', 'duplicate')`. No team filtering. | `identifier`, `title`, `assignee`, `state_type` (selected but not otherwise used by the backtest itself) |
| `aging` (cycle-time distribution) | `started_at`, `completed_at`, `assignee` non-empty¹ | `state_type = 'completed'`; `team_key` filter only if `-teams` given | `identifier`, `title`, `project_name`, `state_name` |
| `aging` (in-progress / WIP ranking) | `started_at` | `state_type = 'started'` AND `started_at IS NOT NULL`; `team_key` filter only if `-teams` given | `identifier`, `title`, `assignee`, `project_name`, `state_name` |
| `cfd` | `created_at`² | `started_at`, `completed_at`, `canceled_at` are each optional per issue (they determine which band/day the issue occupies, not whether it's included); `team_key` filter only if `-teams` given | `state_type` (selected but not currently used by CFD's own logic) |
| `count` | none per-issue — these are aggregate `COUNT(*)`/`MAX()` queries | `state_type NOT IN ('completed', 'canceled', 'duplicate')` for the counts themselves; `updated_at` recency (`-updated-since`, project ordering) is measured across **all** issues, including terminal ones; `team_key` filter only if `-teams` given | `team_name`, `project_name`, `project_milestone_name` (grouping/display keys) |
| `linear sync` | writes all 19 columns | n/a — ingest, not a read | not needed at all if you populate the database yourself |

¹ `aging` reuses `sim`'s `CompletedBetween` query for its cycle-time
distribution, and that query's `WHERE` clause unconditionally requires a
non-empty `assignee` — even though `aging` never filters by a specific
assignee the way `sim -team`/`-typical-engineers` can. A completed issue with
no assignee is silently excluded from the distribution.

² An issue with no `created_at` is dropped entirely: `cfd.Normalize` returns
`ok=false` for it, and the caller is expected to skip it (see
`cmd/forecast/cfd.go`'s skipped-issue count).

## Full schema reference

| Column | Type | Nullable | Meaning |
|---|---|---|---|
| `identifier` | TEXT | NOT NULL (PK) | Unique issue key, e.g. `ENG-123`. Primary key. |
| `title` | TEXT | NOT NULL DEFAULT `''` | Issue title. |
| `assignee` | TEXT | NULL | Assignee name/identifier. NULL when unassigned. |
| `team_key` | TEXT | NOT NULL DEFAULT `''` | Team key (Linear's `team.key`), e.g. `ENG`. Only ever used to filter (`-teams`); never part of a query's selected result. |
| `team_name` | TEXT | NOT NULL DEFAULT `''` | Team display name (Linear's `team.name`). |
| `project_id` | TEXT | NULL | Project ID. NULL when the issue has no project. Written by `linear sync`; not read by any report. |
| `project_name` | TEXT | NULL | Project display name. NULL when the issue has no project. |
| `project_milestone_id` | TEXT | NULL | Milestone ID within the project. NULL when no milestone. Written by `linear sync`; not read by any report. |
| `project_milestone_name` | TEXT | NULL | Milestone display name. NULL when no milestone. |
| `state_type` | TEXT | NOT NULL DEFAULT `''` | Raw workflow state type (Linear's `state.type`), passed through as-is. The codebase only ever tests for the literal values `completed`, `started`, `canceled`, and `duplicate`; any other value passes through untouched (fetched, never specifically matched). |
| `state_name` | TEXT | NOT NULL DEFAULT `''` | Human-readable workflow state name (Linear's `state.name`). |
| `created_at` | DATETIME | NULL | When the issue was created. |
| `started_at` | DATETIME | NULL | When the issue entered a "started" state. NULL if never started. |
| `completed_at` | DATETIME | NULL | When the issue was completed. NULL if not completed. |
| `canceled_at` | DATETIME | NULL | When the issue was canceled. NULL if not canceled. Added for `cfd` support. |
| `archived_at` | DATETIME | NULL | When the issue was archived. Written by `linear sync`; not read by any report. |
| `auto_archived_at` | DATETIME | NULL | When the issue was auto-archived. Written by `linear sync`; not read by any report. |
| `added_to_project_at` | DATETIME | NULL | When the issue was added to its project. Written by `linear sync`; not read by any report. |
| `updated_at` | DATETIME | NULL | Last-modified timestamp. Drives `linear sync`'s incremental watermark and `count`'s `-updated-since` recency filter/ordering. |

## Using this as a library

`simulate`, `aging`, `cfd`, and `counts` live at the repo root (not under
`internal/`), so they're importable on their own —
`github.com/commondatageek/delivery-forecast/simulate` etc. — by callers who
never touch the `issues` table or the `forecast` binary at all. Each package
takes plain structs; build them from whatever source you have.

```go
package main

import (
	"fmt"
	"time"

	"github.com/commondatageek/delivery-forecast/simulate"
)

func main() {
	// Build Completions from your own source (Jira, GitHub, a spreadsheet —
	// anything). Only Engineer and CompletedAt matter.
	completions := []simulate.Completion{
		{Engineer: "alice", CompletedAt: time.Date(2025, 6, 2, 0, 0, 0, 0, time.Local)},
		{Engineer: "alice", CompletedAt: time.Date(2025, 6, 5, 0, 0, 0, 0, time.Local)},
		{Engineer: "bob", CompletedAt: time.Date(2025, 6, 3, 0, 0, 0, 0, time.Local)},
		// ...
	}

	start := time.Date(2025, 6, 1, 0, 0, 0, 0, time.Local) // local midnight — see simulate's doc comment
	end := time.Date(2025, 7, 1, 0, 0, 0, 0, time.Local)   // half-open: excludes July 1 itself

	pool := simulate.BuildPool(completions, simulate.Exclusions{}, start, end, false)

	dist := simulate.ItemsInDays(pool, simulate.Params{
		Mode:        simulate.ModeAnonymous,
		Engineers:   3,
		Days:        30,
		Simulations: 10_000,
		Workers:     4,
		Seed:        42,
	})

	fmt.Printf("p50: %d items in 30 days\n", simulate.PercentileValue(dist, 50))
}
```

`aging`, `cfd`, and `counts` follow the same shape: build their neutral input
records (`aging.Issue`, `cfd.Issue`, `counts.ProjectMilestoneCount` /
`ProjectActivity`) from your own data, then call the package's exported
functions directly — `aging.CycleTimes`/`InProgressItems`/`RankItems`,
`cfd.Normalize`/`BuildGrid`/`ComputeHealth`, `counts.Compute`. None of them
touch a file, network, or database, and none of them call `time.Now`. See
each package's doc comment (`go doc ./aging`, `go doc ./cfd`, `go doc
./counts`) for its exact inputs and semantics.
