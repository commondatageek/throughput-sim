import json
import random
from datetime import date, timedelta

engineers = ["alice", "bob", "carol", "dave"]

# Give each engineer a different throughput profile
# (avg issues per day, with some variance)
throughput = {
    "alice": (1.2, 0.8),
    "bob": (0.6, 0.5),
    "carol": (1.8, 1.0),
    "dave": (0.9, 0.6),
}

start = date(2024, 1, 1)
end = date(2024, 12, 31)

# Dates nobody works
global_excluded = {
    date(2024, 12, 23),
    date(2024, 12, 24),
    date(2024, 12, 25),
    date(2024, 12, 26),
    date(2024, 12, 27),
    date(2024, 1, 1),
    date(2024, 7, 4),
    date(2024, 11, 28),
}

# Per-engineer time off (rough approximation)
pto = {
    "alice": {date(2024, 6, 17), date(2024, 6, 18), date(2024, 6, 19)},
    "bob": {
        date(2024, 3, 11),
        date(2024, 3, 12),
        date(2024, 3, 13),
        date(2024, 3, 14),
        date(2024, 3, 15),
    },
    "carol": {
        date(2024, 8, 5),
        date(2024, 8, 6),
        date(2024, 8, 7),
        date(2024, 8, 8),
        date(2024, 8, 9),
    },
    "dave": {date(2024, 2, 19), date(2024, 2, 20)},
}

issues = []
issue_num = 1

current = start
while current <= end:
    is_weekend = current.weekday() >= 5
    is_global_excluded = current in global_excluded

    for eng in engineers:
        is_pto = current in pto.get(eng, set())
        if is_weekend or is_global_excluded or is_pto:
            current_date = current  # still advance, just skip
            pass
        else:
            avg, std = throughput[eng]
            count = max(0, round(random.gauss(avg, std)))
            for _ in range(count):
                issues.append(
                    {
                        "engineer": eng,
                        "title": f"Issue #{issue_num}: some work item",
                        "completed_at": f"{current.isoformat()}T17:00:00Z",
                    }
                )
                issue_num += 1

    current += timedelta(days=1)

random.shuffle(issues)
for issue in issues:
    print(json.dumps(issue))
