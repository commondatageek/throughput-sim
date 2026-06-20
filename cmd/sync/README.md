# sync

Syncs issues from Linear into the SQLite store (`linear.db`), consumed by
`sim` and `aging-report`. Database-centric and per-team: each candidate team
is synced against its own `team_key` watermark, with a commit after each team
so a failure partway through (e.g. rate limiting) leaves already-synced teams
resumable next run.

## Build

```sh
go build -o sync .
```

## Requirements

Requires the `LINEAR_API_KEY` environment variable to be set.

## Usage

```sh
sync [-teams k1,k2] [-all-teams] [-full-reload] <db-path>
```

Flags must precede the positional `<db-path>` argument (standard Go `flag`
package ordering).

| Flag | Default | Description |
|---|---|---|
| `-teams` | none | Comma-separated team keys, e.g. `ENG,DESIGN`. Limits/extends the candidate team set; new keys get a full sync |
| `-all-teams` | `false` | Expand the candidate set to every accessible Linear team. Mutually exclusive with `-teams` |
| `-full-reload` | `false` | Ignore each team's stored watermark and full-sync every candidate team |

With no `-teams`/`-all-teams`, sync incrementally syncs every team already
present in the database. A brand-new/empty database has no existing teams to
fall back on, so it requires `-teams` or `-all-teams` to seed it.

## Examples

Incrementally sync every team already in the database:

```sh
./sync linear.db
```

Seed a new database with two teams:

```sh
./sync -teams ENG,DESIGN linear.db
```

Sync every accessible Linear team, expanding the candidate set beyond what's
already stored:

```sh
./sync -all-teams linear.db
```

Full-reload every candidate team, ignoring stored watermarks:

```sh
./sync -all-teams -full-reload linear.db
```

## Output

Progress is logged to stderr via `log/slog` (team, sync mode, watermark,
upserted issue count). Exits non-zero on error.
