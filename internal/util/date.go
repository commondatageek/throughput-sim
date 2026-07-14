package util

import "time"

// ParseDate parses a YYYY-MM-DD calendar date in the machine's local timezone.
// User-entered dates are interpreted as local wall-clock days so they match the
// user's mental model (Linear data stored in the DB stays UTC; see sqlite).
func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.Local)
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
