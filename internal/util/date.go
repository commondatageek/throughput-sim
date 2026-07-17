package util

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseDate parses a YYYY-MM-DD calendar date in the machine's local timezone.
// User-entered dates are interpreted as local wall-clock days so they match the
// user's mental model (Linear data stored in the DB stays UTC; see sqlite).
func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

// relativeDateRe matches a relative offset like "-3 months", "+2 weeks",
// "90 days", or "3 months ago". Group 1 is an optional sign, 2 the amount, 3 the
// unit (singular or plural), 4 the optional " ago" suffix.
var relativeDateRe = regexp.MustCompile(`^([+-]?)\s*(\d+)\s*(day|week|month|year)s?(\s+ago)?$`)

// ParseFlexibleDate parses a user-supplied date flag value, anchored to now. It
// accepts, in addition to ParseDate's YYYY-MM-DD:
//
//   - the keyword "now", resolving to the exact instant passed as now (see the
//     "now" is an instant, not a day" note below);
//   - the keywords "yesterday", "today", and "tomorrow";
//   - relative offsets: "-3 months", "+2 weeks", "90 days", "3 months ago"
//     (units: day, week, month, year, singular or plural). A leading "+" means
//     the future; a leading "-", a trailing "ago", or no sign at all means the
//     past. "+... ago" is contradictory and rejected.
//
// Every result except "now" snaps to local midnight of the resolved calendar
// day (matching ParseDate), so callers get consistent whole-day semantics
// regardless of the input form. "now" is deliberately the one exception: it
// names a precise instant, not a calendar day, so it passes now through
// unmodified — this is what lets a flag like -sample-end default to the
// literal string "now" and still count a day's work completed earlier that
// same day.
//
// That said, "now"'s non-midnight instant is only safe for bounds that are
// merely *compared against* (typically a window's end/upper bound). Bounds
// used as the anchor for day-bucketing (util.DayIndex's start argument —
// typically a window's start) must stay midnight-aligned, or same-day records
// silently misbucket. Callers resolving a start-type bound should use
// ParseFlexibleStartDate instead, which rejects "now" outright. now is taken
// as a parameter (rather than calling time.Now) so the resolution is
// deterministic and testable.
func ParseFlexibleDate(s string, now time.Time) (time.Time, error) {
	trimmed := strings.ToLower(strings.TrimSpace(s))

	if trimmed == "now" {
		return now, nil
	}

	today := LocalDay(now)

	switch trimmed {
	case "yesterday":
		return today.AddDate(0, 0, -1), nil
	case "today":
		return today, nil
	case "tomorrow":
		return today.AddDate(0, 0, 1), nil
	}

	if m := relativeDateRe.FindStringSubmatch(trimmed); m != nil {
		sign, unit, ago := m[1], m[3], m[4] != ""
		if sign == "+" && ago {
			return time.Time{}, fmt.Errorf("invalid date %q: %q and \"ago\" are contradictory", s, sign)
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid date %q: %w", s, err)
		}
		if sign != "+" { // "-", "" (bare = past), or "... ago" all mean the past
			n = -n
		}
		switch unit {
		case "day":
			return today.AddDate(0, 0, n), nil
		case "week":
			return today.AddDate(0, 0, 7*n), nil
		case "month":
			return today.AddDate(0, n, 0), nil
		default: // "year"
			return today.AddDate(n, 0, 0), nil
		}
	}

	return ParseDate(s)
}

// ParseFlexibleStartDate is ParseFlexibleDate for start-type window bounds —
// -sample-start, -target-start-date, -replay-start-date, -start (cfd), and the
// like. It rejects the literal "now" (case-insensitive) with a clear error;
// every other input is delegated to ParseFlexibleDate unchanged.
//
// "now" as a start bound is a real footgun, not just a style objection:
// downstream day-bucketing (util.DayIndex, used as start's anchor by
// simulate.BuildPool/DaysBetween and simulate.RunBacktest) rounds a
// day-vs-start gap to the nearest 24h. A "now" start carries a time-of-day
// offset, so a same-day record earlier than "now" can round to the prior day
// and silently fall outside the window — data loss with no error. Reject it
// here, once, rather than requiring every start-type call site to know why.
func ParseFlexibleStartDate(s string, now time.Time) (time.Time, error) {
	if strings.EqualFold(strings.TrimSpace(s), "now") {
		return time.Time{}, fmt.Errorf(`"now" is not a valid start date (window starts must align to a calendar day); use "today" instead`)
	}
	return ParseFlexibleDate(s, now)
}

// LocalDay returns local midnight of t's local calendar day. A zero time is
// returned unchanged.
//
// This exists because t.Truncate(24*time.Hour) always snaps to *UTC* midnight
// regardless of t's location (Truncate operates on absolute duration since the
// zero instant), so it cannot produce a local calendar day. We rebuild the time
// from the local Y/M/D components instead.
func LocalDay(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	y, m, d := t.Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// DayIndex returns the whole-day offset from start to day. Both arguments are
// expected to be local-midnight instants (e.g. from LocalDay or ParseDate).
//
// It rounds to whole days so a window crossing a DST change (which spans
// n*24h ± 1h) still yields the exact day count rather than truncating off by
// one. Works for negative offsets too.
func DayIndex(day, start time.Time) int {
	return int(day.Sub(start).Round(24*time.Hour) / (24 * time.Hour))
}
