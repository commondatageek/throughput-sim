# delivery-forecast

A Linear-only delivery-forecasting toolkit. It syncs issues from
[Linear](https://linear.app) into a local SQLite database, then runs
Monte Carlo forecasts and cycle-time/flow reports against that data — all
from a single `forecast` binary.

```
linear.Client  --Fetch-->  linear.Issue  --Upsert-->  sqlite.Store (linear.db, "issues" table)
                                                               |
                                         +---------------------+----------------------+
                                         |                                            |
                                  forecast sim                              forecast aging/cfd/count
                            (Monte Carlo forecasts)                   (cycle-time / WIP-age / CFD reports)
```

## Install

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/commondatageek/delivery-forecast/main/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/commondatageek/delivery-forecast/main/install.ps1 | iex
```

Both scripts download the release asset matching your OS/arch, verify its
SHA256 checksum, and install `forecast` to `~/.forecast/bin`
(`%USERPROFILE%\.forecast\bin` on Windows), adding it to your `PATH` unless
opted out.

| Env var | Default | Description |
|---|---|---|
| `FORECAST_INSTALL_DIR` | `~/.forecast/bin` | where to install the binary |
| `FORECAST_VERSION` | latest release | pin a specific release tag, e.g. `v1.2.3` |
| `FORECAST_NO_MODIFY_PATH` | unset | set to skip editing your shell profile / User `PATH` |

To pin a version (or set any other env var above) with the piped `install.sh`
one-liner, put the assignment **after** the pipe, on the `sh` side — not
before `curl`. `curl` and `sh` are separate processes joined by a pipe, so a
prefix like `FORECAST_VERSION=v1.2.3 curl ... | sh` only sets the variable
for `curl` and `sh` never sees it, silently installing latest instead:

```bash
# correct — the env var reaches sh, which is what reads it
curl -fsSL https://raw.githubusercontent.com/commondatageek/delivery-forecast/main/install.sh | FORECAST_VERSION=v1.2.3 sh

# also correct
export FORECAST_VERSION=v1.2.3
curl -fsSL https://raw.githubusercontent.com/commondatageek/delivery-forecast/main/install.sh | sh
```

(PowerShell's `irm | iex` doesn't have this issue — `$env:FORECAST_VERSION = "v1.2.3"` set beforehand works fine since both run in the same session.)

Prefer to install manually? Grab the archive for your platform from the
[releases page](https://github.com/commondatageek/delivery-forecast/releases)
and extract the `forecast` binary onto your `PATH` yourself.

Once installed, `forecast update` (below) handles future upgrades in place.

## Build & test

Uses [Just](https://just.systems):

```bash
just build       # compiles bin/forecast
just test        # go test ./...
```

Or plain Go:

```bash
go build -o bin/forecast ./cmd/forecast
go test ./...
```

Run `forecast` with no arguments to see the full command list.

## Linear ingest

`forecast linear sync` and `forecast linear teams` require a
`LINEAR_API_KEY` environment variable (a Linear personal API key).

```bash
export LINEAR_API_KEY=lin_api_...
forecast linear teams
forecast linear sync -db linear.db -all-teams
```

A brand-new/empty database needs `-teams` or `-all-teams` to seed it; after
that, `forecast linear sync -db linear.db` alone will incrementally sync
every team already in the db, each against its own watermark.

| Flag | Default | Description |
|---|---|---|
| `-db` | *(required)* | path to SQLite database |
| `-teams` | | comma-separated team keys, e.g. ENG,DESIGN; limits the candidate team set |
| `-all-teams` | `false` | expand the candidate team set to every accessible Linear team; mutually exclusive with `-teams` |
| `-full-reload` | `false` | ignore each team's stored watermark and do a full reload |
| `-config` | | path to a YAML config file supplying flag values (CLI flags override) |

`forecast linear teams` just lists accessible teams (key, name) and exits; it takes only `-config`.

## `forecast sim` — Monte Carlo forecasting

Four subcommands, all sampling from the same historical daily-completion
data (`-sample-start`/`-sample-end`) in one of three mutually-exclusive
modes:

One of the three is required — there is no implicit default mode:

- `-engineers N` — pool all engineers' history together and draw for N anonymous equivalent engineers.
- `-team alice,bob` — each named engineer draws from their own history.
- `-whole-team` — sum all engineers' daily counts into one series (ignores individual variance).

### `sim items` — how many items in D days?

```bash
forecast sim items -db linear.db -team alice,bob -days 30
```

| Flag | Default | Description |
|---|---|---|
| `-db` | *(required)* | path to SQLite database |
| `-exclusions` | `exclusions.json` | path to exclusions JSON file |
| `-engineers` | *(required unless `-team`/`-whole-team`)* | number of (equivalent) engineers |
| `-days` | `30` | number of days |
| `-whole-team` | `false` | use whole-team daily throughput from historical data (ignores `-engineers`) |
| `-simulations` | `10000` | number of Monte Carlo simulations to run |
| `-goroutines` | NumCPU | number of parallel worker goroutines |
| `-sample-start` | 3 months ago | sample data start date (YYYY-MM-DD) |
| `-sample-end` | now | sample data end date (YYYY-MM-DD) |
| `-random-seed` | time-based | seed for the random number generator |
| `-percentile` | `5,25,50,75,95` | comma-separated percentiles to output |
| `-typical-engineers` | all | comma-separated list of the team's typical engineers to build the sample pool from |
| `-team` | | comma-separated list of specific engineer names to model individually |
| `-manifest` | | write a run-provenance JSON manifest to this path (`-` for stdout) |
| `-config` | | path to a YAML config file supplying flag values (CLI flags override) |

### `sim days` — how many days to finish I items?

```bash
forecast sim days -db linear.db -whole-team -items 50
```

Same flags as `sim items`, plus:

| Flag | Default | Description |
|---|---|---|
| `-items` | *(required)* | number of items to complete; comma-separated for a grouped trajectory report (e.g. `13,12,9`) |
| `-target-start-date` | `today` | forecast start date used to compute calendar dates (YYYY-MM-DD, or: today, tomorrow) |
| `-percentile` | `50,75,85,95` | comma-separated percentiles to output |

(no `-days` flag — that's `sim items`'s target quantity.)

### `sim probability` — probability of completing I items in D days?

```bash
forecast sim probability -db linear.db -engineers 4 -days 30 -items 40
```

Same base flags as `sim items` (minus `-percentile`), plus:

| Flag | Default | Description |
|---|---|---|
| `-days` | | number of days; mutually exclusive with `-target-end-date`, one must be given |
| `-target-start-date` | `tomorrow` | start of the target window (YYYY-MM-DD, or: today, tomorrow) |
| `-target-end-date` | | end of the target window (YYYY-MM-DD, or: today, tomorrow); mutually exclusive with `-days`, one must be given |
| `-items` | `-1` | number of items to complete (omit, i.e. leave at -1, to show the full distribution) |

### `sim backtest` — replay probability forecasts day-by-day

Replays `sim probability`-style forecasts against a project/milestone's
actual history, one row per day from the replay start date to a completion
deadline.

```bash
forecast sim backtest -db linear.db -whole-team -project "Q3 Migration" -target-end-date 2025-09-30
```

Same base sampling flags as `sim items`, plus:

| Flag | Default | Description |
|---|---|---|
| `-project` | *(required)* | project name to backtest |
| `-milestone` | | milestone name within the project (optional) |
| `-replay-start-date` | earliest `started_at` in the issue set | first day to replay from, inclusive (YYYY-MM-DD) |
| `-target-end-date` | *(required)* | completion deadline to forecast against (YYYY-MM-DD) |
| `-format` | `text` | output format: `text` or `csv` |

Note: `-simulations` here means "simulations per backtested day" (same default, `10000`).

## `forecast aging` — WIP-age / cycle-time report

Computes the historical cycle-time distribution from completed issues, then
ranks currently in-progress issues by percentile against that distribution.

```bash
forecast aging -db linear.db -format html > aging.html
```

| Flag | Default | Description |
|---|---|---|
| `-db` | *(required)* | path to SQLite database |
| `-sample-start` | today minus 3 months | start of completed-issue window (YYYY-MM-DD) |
| `-sample-end` | today | end of completed-issue window (YYYY-MM-DD) |
| `-format` | `text` | output format: `text`, `json`, `html` |
| `-min-cycle-time` | | exclude completed issues with cycle time below this duration (e.g. `5m`, `1h`, `1d`) |
| `-teams` | all teams | comma-separated team keys to filter by (e.g. DATA,PLT) |
| `-config` | | path to a YAML config file supplying flag values (CLI flags override) |

## `forecast cfd` — Cumulative Flow Diagram

Builds a 4-line / 3-band CFD (Created, LeftBacklog, Departed, Completed)
and flow-health stats (throughput, avg WIP, cycle time, Little's Law
cross-check). Renders an interactive Plotly HTML chart by default, or a
daily-series JSON.

```bash
forecast cfd -db linear.db -start 2025-01-01 -end 2025-07-01 -out cfd.html
```

| Flag | Default | Description |
|---|---|---|
| `-db` | *(required)* | path to SQLite database |
| `-teams` | all teams | comma-separated team keys to filter by (e.g. ENG,DATA) |
| `-start` | today minus 3 months | start date, inclusive (YYYY-MM-DD) |
| `-end` | today | end date, inclusive (YYYY-MM-DD) |
| `-format` | `html` | output format: `html`, `json` |
| `-out` | stdout | write output to this file instead of stdout |
| `-config` | | path to a YAML config file supplying flag values (CLI flags override) |

## `forecast count` — outstanding-work report

Counts non-terminal issues (not `completed`/`canceled`/`duplicate`), grouped
by project (and optionally milestone). Read-only.

```bash
forecast count -db linear.db -milestones
```

| Flag | Default | Description |
|---|---|---|
| `-db` | *(required)* | path to SQLite database |
| `-milestones` | `false` | add a per-milestone breakdown under each project |
| `-updated-since` | today minus 3 months | only include projects with an issue updated on/after this date (YYYY-MM-DD) |
| `-teams` | all teams | comma-separated team keys to filter by (e.g. ENG,DESIGN) |
| `-config` | | path to a YAML config file supplying flag values (CLI flags override) |

## `forecast version` — print version and build info

```bash
forecast version
```

Prints the build-time version (set via `-ldflags "-X main.version=..."` for
released binaries, `(dev)` for local builds) plus VCS-stamped build info:
git SHA, git time, dirty flag, Go version, module.

## `forecast update` — self-update

Checks the latest GitHub release, compares it against the running binary's
version, and — after confirmation — downloads, checksum-verifies, and
installs the release asset matching this OS/arch, replacing the running
executable in place.

```bash
forecast update -check   # report current vs. latest, install nothing
forecast update -yes     # download, verify, and install without prompting
```

| Flag | Default | Description |
|---|---|---|
| `-check` | `false` | report current vs. latest version and exit without installing anything |
| `-yes` | `false` | skip the interactive confirmation prompt |
| `-force` | `false` | proceed even if already on the latest version, or the current version is unknown (a dev build) |
| `-timeout` | `60s` | overall HTTP timeout |

## Config files (`-config`)

Every subcommand accepts `-config <file.yaml>` to supply flag values from a
YAML file, applied immediately after flag parsing. Precedence is **CLI flag
> config file > built-in default**.

- Keys equal flag names exactly as passed on the command line (e.g.
  `-sample-end` → `sample-end`).
- List flags (`-teams`, `-team`, `-typical-engineers`, `-percentile`, `-items`) take a
  YAML sequence, joined into the same comma-separated string the flag
  itself accepts: `teams: [ENG, DATA]` behaves identically to
  `-teams ENG,DATA`. A plain string (`teams: "ENG,DATA"`) also works.
- Presence-sensitive flags behave as if passed on the CLI: a `sample-end` or
  `random-seed` set only in a config file still counts as "explicitly set"
  — e.g. `random-seed: 42` in a config pins the seed exactly like
  `-random-seed 42` would, rather than falling back to the time-based
  default.
- The `config` key itself is reserved/ignored inside the file.
- One config file's keys are shared by exactly one command's flags — there's
  no per-command sectioning (a `sim items` config and a `count` config are
  separate files).

Example for `forecast sim items` (`sim-items.yaml`):

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

## `exclusions.json`

Optional input to `forecast sim` (e.g. for holidays), pointed at via
`-exclusions` (default: `exclusions.json` in the working directory):

```json
{
  "global": ["2024-12-25"],
  "engineers": {"alice": ["2024-06-17"]}
}
```

`global` dates are excluded for every engineer; `engineers` dates are
excluded only for the named engineer.

## Using as a library

The `simulate`, `aging`, `cfd`, and `counts` packages are pure, IO-free, and
independent of Linear/SQLite, so they're importable on their own —
`github.com/commondatageek/delivery-forecast/simulate`, etc. — by anyone who
wants the same Monte Carlo forecasting or cycle-time/CFD/count analysis over
data from another source. See [DATA_REQUIREMENTS.md](DATA_REQUIREMENTS.md)
for what each package needs from your data and a short library-usage example.

## Conventions worth knowing

- `-sample-end`: if explicitly set, it's a calendar date (midnight, that day
  excluded). If omitted, it defaults to *now*, so today's already-completed
  work counts.
- `-random-seed`: time-based (non-deterministic) unless explicitly passed —
  either on the CLI or via `-config`.

## License

Copyright 2026 Aaron Johnson.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the
full text.
