# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Uses [Task](https://taskfile.dev) for automation (`task` CLI required):

```bash
task build       # Compile Go binaries to bin/
task generate    # Generate synthetic issues.json via Python
task fetch       # Fetch real data from Linear (requires LINEAR_API_KEY env var)
```

Manual builds:
```bash
go build -C sim -o ../bin/sim .
go build -C linear-fetch -o ../bin/linear-fetch .
```

There are no automated tests or linting configured.

## Architecture

Three independent components sharing a common newline-delimited JSON data format:

**`generate-issues/main.py`** — Python script that generates synthetic historical issue data for testing. Outputs to stdout. No dependencies.

**`linear-fetch/main.go`** — Fetches completed issues from the Linear.app GraphQL API (paginated). Reads `LINEAR_API_KEY` from environment, outputs newline-delimited JSON to stdout, logs progress to stderr.

**`sim/main.go`** — The core Monte Carlo simulation engine. Three subcommands:
- `items` — how many items can N engineers complete in D days?
- `days` — how many days for N engineers to complete I items?
- `probability` — what's the probability of completing I items in D days?

It reads `issues.json` and `exclusions.json`, builds a `SamplePool` of historical daily completion counts per engineer, then runs N simulations (default 10,000) by randomly sampling from that pool.

### Data formats

**`issues.json`** (input to `sim`, output of `linear-fetch` and `generate-issues`):
```json
{"engineer": "alice", "title": "Fix bug", "completed_at": "2025-08-01T14:00:00Z"}
```

**`exclusions.json`** (optional input to `sim`):
```json
{
  "global": ["2024-12-25"],
  "engineers": {"alice": ["2024-06-17"]}
}
```

### Go workspace

`go.work` defines a multi-module workspace with two modules: `sim/` and `linear-fetch/`. Neither has external dependencies.

### Key `sim` flags

| Flag | Description |
|------|-------------|
| `-issues` | Input data file path |
| `-exclusions` | Exclusion dates file path |
| `-engineers` | Team size (default: 3) |
| `-days` / `-items` | Simulation parameters |
| `-simulations` | Monte Carlo runs (default: 10,000) |
| `-sample-start` / `-sample-end` | Historical data window |
| `-percentile` | Which percentiles to output |
| `-include` | Filter to specific engineers (comma-separated) |

### Example usage

```bash
./bin/sim items -engineers 4 -days 30 -issues issues.json
./bin/sim days -engineers 3 -items 80 -issues issues.json
./bin/sim probability -engineers 5 -days 14 -items 40 -issues issues.json
LINEAR_API_KEY=... ./bin/linear-fetch -teams ENG > issues.json
```

## On-call modeling

`ONCALL_MODELING.md` documents a planned (not yet implemented) feature to model on-call rotations. Two design options are discussed: a `-oncall-fraction` flag vs. separate sample pools for on-call vs. normal days.
