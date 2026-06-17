#!/usr/bin/env bash
# Sanity-check completed-issue data for a set of engineers before trusting a
# `sim` run. For each engineer, reports how many completed items fall in the
# sample window, how many distinct days have at least one completion, and
# how many days in the window would be a 0 in the sample pool. Also reports
# each engineer's lifetime first/last completed_at so you can tell "thin in
# this window" apart from "no real data at all" or "name doesn't match".
#
# Mirrors `sim`'s date semantics: -start is inclusive, -end is exclusive
# (i.e. pass the day *after* the last day you want, same as -sample-end).
#
# Usage:
#   scripts/check-engineer-data.sh -db all.db -engineers "alice,Bob Jones" \
#     -start 2026-01-05 -end 2026-04-01
#
# Flags:
#   -db          path to the sqlite database (default: items.db)
#   -engineers   comma-separated list of assignee names to check (required)
#   -start       sample window start, YYYY-MM-DD, inclusive (default: 90 days before -end)
#   -end         sample window end, YYYY-MM-DD, exclusive (default: today)

set -euo pipefail

db="items.db"
engineers=""
start=""
end=""

while [ $# -gt 0 ]; do
  case "$1" in
    -db) db="$2"; shift 2 ;;
    -engineers) engineers="$2"; shift 2 ;;
    -start) start="$2"; shift 2 ;;
    -end) end="$2"; shift 2 ;;
    -h|-help|--help)
      sed -n '2,20p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 1 ;;
  esac
done

if [ -z "$engineers" ]; then
  echo "error: -engineers is required (comma-separated list)" >&2
  exit 1
fi

if [ ! -f "$db" ]; then
  echo "error: db not found: $db" >&2
  exit 1
fi

end="${end:-$(date -u +%Y-%m-%d)}"
start="${start:-$(date -j -u -v-90d -f '%Y-%m-%d %H:%M:%S' "$end 00:00:00" +%Y-%m-%d)}"

start_epoch=$(date -j -u -f '%Y-%m-%d %H:%M:%S' "$start 00:00:00" +%s)
end_epoch=$(date -j -u -f '%Y-%m-%d %H:%M:%S' "$end 00:00:00" +%s)
total_days=$(((end_epoch - start_epoch) / 86400))

if [ "$total_days" -le 0 ]; then
  echo "error: -start must be before -end" >&2
  exit 1
fi

IFS=',' read -r -a engineer_list <<< "$engineers"

# Builds a SQL IN-list literal like 'a','b','c' from engineer_list, escaping
# embedded single quotes by doubling them.
sql_in_list() {
  local sep="" e escaped
  for e in "${engineer_list[@]}"; do
    escaped="${e//\'/\'\'}"
    printf "%s'%s'" "$sep" "$escaped"
    sep=","
  done
}
in_list="$(sql_in_list)"

echo "Window: [$start, $end) -- $total_days days"
echo

echo "=== Engineers not found in data at all (check for typos) ==="
any_missing=0
for eng in "${engineer_list[@]}"; do
  escaped="${eng//\'/\'\'}"
  count=$(sqlite3 "$db" "SELECT COUNT(*) FROM items WHERE assignee = '$escaped';")
  if [ "$count" -eq 0 ]; then
    echo "  MISSING: $eng"
    any_missing=1
  fi
done
if [ "$any_missing" -eq 0 ]; then
  echo "  (none -- all engineers found)"
fi
echo

echo "=== Completed items in window, distinct days with a completion, and resulting 0-count days ==="
sqlite3 -header -column "$db" "
SELECT assignee,
       COUNT(*) AS items_in_window,
       COUNT(DISTINCT substr(completed_at,1,10)) AS distinct_days,
       $total_days - COUNT(DISTINCT substr(completed_at,1,10)) AS zero_days_in_pool
FROM items
WHERE status = 'completed'
  AND assignee IN ($in_list)
  AND completed_at >= '$start'
  AND completed_at <  '$end'
GROUP BY assignee
ORDER BY assignee;
"
echo

echo "=== Lifetime completed-item history per engineer (sanity check there's real data behind the window) ==="
sqlite3 -header -column "$db" "
SELECT assignee,
       COUNT(*) AS total_completed_ever,
       MIN(completed_at) AS earliest,
       MAX(completed_at) AS latest
FROM items
WHERE status = 'completed'
  AND assignee IN ($in_list)
GROUP BY assignee
ORDER BY assignee;
"
