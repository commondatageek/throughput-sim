package main

import (
	"flag"
	"testing"
	"time"

	"forecasting/internal/simulate"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// newSampleEndFlags returns a FlagSet defining the flags resolveEndDate and
// resolveSeed inspect. The flags are only registered as "set" via fs.Set, which
// is exactly what isFlagSet (backed by fs.Visit) keys off of.
func newSampleEndFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("sample-end", "", "")
	fs.Int64("random-seed", 0, "")
	return fs
}

func TestResolveEndDate(t *testing.T) {
	start := day(2025, 1, 1)
	// Mid-afternoon "now": the unset branch must return this verbatim so that
	// simulate.DaysBetween counts today as a partial, inclusive slot (the +1 branch).
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.UTC)

	// Unset -sample-end: defaults to now, today included as a partial slot.
	fs := newSampleEndFlags()
	end, err := resolveEndDate(fs, "", now)
	if err != nil {
		t.Fatalf("resolveEndDate (unset) error: %v", err)
	}
	if !end.Equal(now) {
		t.Errorf("resolveEndDate (unset) = %v, want now %v", end, now)
	}
	if got := simulate.DaysBetween(start, end); got != 5 {
		t.Errorf("DaysBetween with default now: got %d, want 5 (today is a partial slot)", got)
	}

	// Explicit -sample-end: parsed as a midnight calendar date, excluding that
	// whole day and ignoring now.
	fs = newSampleEndFlags()
	if err := fs.Set("sample-end", "2025-01-05"); err != nil {
		t.Fatal(err)
	}
	end, err = resolveEndDate(fs, "2025-01-05", now)
	if err != nil {
		t.Fatalf("resolveEndDate (set) error: %v", err)
	}
	if !end.Equal(day(2025, 1, 5)) {
		t.Errorf("resolveEndDate (set) = %v, want midnight 2025-01-05", end)
	}
	if got := simulate.DaysBetween(start, end); got != 4 {
		t.Errorf("DaysBetween with explicit midnight end: got %d, want 4", got)
	}
}

func TestResolveSeed(t *testing.T) {
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.UTC)

	// Unset -random-seed: time-based seed derived from now.
	fs := newSampleEndFlags()
	if got := resolveSeed(fs, 99, now); got != now.UnixNano() {
		t.Errorf("resolveSeed (unset) = %d, want %d", got, now.UnixNano())
	}

	// Explicit -random-seed: returned verbatim, now ignored.
	fs = newSampleEndFlags()
	if err := fs.Set("random-seed", "99"); err != nil {
		t.Fatal(err)
	}
	if got := resolveSeed(fs, 99, now); got != 99 {
		t.Errorf("resolveSeed (set) = %d, want 99", got)
	}
}
