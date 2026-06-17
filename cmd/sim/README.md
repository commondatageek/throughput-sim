# sim

Monte Carlo throughput simulator. Reads historical issue completion data from a
JSON file and runs simulations to answer three types of questions.

## Build

```sh
go build -o sim .
```

## Input format

`issues.json` is a newline-delimited stream of JSON objects:

```json
{"engineer": "alice", "title": "Fix login bug", "completed_at": "2025-08-01T14:00:00Z"}
{"engineer": "bob", "title": "Add dark mode", "completed_at": "2025-08-02T09:30:00Z"}
```

## Subcommands

### `items` — how many items can N engineers complete in D days?

```sh
./sim items \
  -issues issues.json \
  -engineers 4 \
  -days 30 \
  -simulations 10000 \
  -sample-start 2025-02-01 \
  -sample-end 2025-08-01 \
  -percentile 50,75,90,95 \
  -include "alice,bob,carol"
```

| Flag | Default | Description |
|---|---|---|
| `-issues` | `issues.json` | Path to issues JSON file |
| `-engineers` | `3` | Number of engineers |
| `-days` | `30` | Number of days to simulate |
| `-simulations` | `10000` | Number of Monte Carlo simulations |
| `-sample-start` | 6 months ago | Start of historical sample window (YYYY-MM-DD) |
| `-sample-end` | today | End of historical sample window (YYYY-MM-DD) |
| `-percentile` | `5,10,...,95` | Comma-separated percentiles to output |
| `-include` | all | Comma-separated engineer names to include |

### `days` — how many days for N engineers to complete I items?

```sh
./sim days \
  -issues issues.json \
  -engineers 4 \
  -items 80 \
  -simulations 10000 \
  -sample-start 2025-02-01 \
  -sample-end 2025-08-01 \
  -percentile 50,75,90,95 \
  -include "alice,bob,carol"
```

| Flag | Default | Description |
|---|---|---|
| `-issues` | `issues.json` | Path to issues JSON file |
| `-engineers` | `3` | Number of engineers |
| `-items` | `50` | Number of items to complete |
| `-simulations` | `10000` | Number of Monte Carlo simulations |
| `-sample-start` | 6 months ago | Start of historical sample window (YYYY-MM-DD) |
| `-sample-end` | today | End of historical sample window (YYYY-MM-DD) |
| `-percentile` | `5,10,...,95` | Comma-separated percentiles to output |
| `-include` | all | Comma-separated engineer names to include |

### `probability` — what is the probability of completing I items in D days?

```sh
./sim probability \
  -issues issues.json \
  -engineers 4 \
  -days 30 \
  -items 80 \
  -simulations 10000 \
  -sample-start 2025-02-01 \
  -sample-end 2025-08-01 \
  -include "alice,bob,carol"
```

| Flag | Default | Description |
|---|---|---|
| `-issues` | `issues.json` | Path to issues JSON file |
| `-engineers` | `3` | Number of engineers |
| `-days` | `30` | Number of days to simulate |
| `-items` | `50` | Number of items to complete |
| `-simulations` | `10000` | Number of Monte Carlo simulations |
| `-sample-start` | 6 months ago | Start of historical sample window (YYYY-MM-DD) |
| `-sample-end` | today | End of historical sample window (YYYY-MM-DD) |
| `-include` | all | Comma-separated engineer names to include |
