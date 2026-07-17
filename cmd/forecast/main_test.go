package main

import (
	"flag"
	"testing"
	"time"

	"forecasting/internal/simulate"
	"forecasting/internal/util"
)

// day builds a local-midnight calendar date. Fixtures use time.Local (not UTC)
// so they share the zone used by DaysBetween's local-day bucketing, keeping the
// expected-value assertions correct regardless of the machine's timezone.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// newRandomSeedFlags returns a FlagSet defining the flag resolveSeed inspects.
// It's only registered as "set" via fs.Set, which is exactly what isFlagSet
// (backed by fs.Visit) keys off of.
func newRandomSeedFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Int64("random-seed", 0, "")
	return fs
}

// TestSampleEndNow_DaysBetween pins the invariant -sample-end's "now" default
// exists for: a mid-afternoon "now", parsed via util.ParseFlexibleDate (as
// -sample-end's literal "now" default now is, in place of the old
// isFlagSet-gated resolveEndDate), must come back as the exact instant so
// simulate.DaysBetween counts today as a partial, inclusive slot. An explicit
// calendar date, in contrast, snaps to midnight and excludes that whole day.
func TestSampleEndNow_DaysBetween(t *testing.T) {
	start := day(2025, 1, 1)
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.Local)

	end, err := util.ParseFlexibleDate("now", now)
	if err != nil {
		t.Fatalf("ParseFlexibleDate(now) error: %v", err)
	}
	if !end.Equal(now) {
		t.Errorf("ParseFlexibleDate(now) = %v, want now %v", end, now)
	}
	if got := simulate.DaysBetween(start, end); got != 5 {
		t.Errorf("DaysBetween with now: got %d, want 5 (today is a partial slot)", got)
	}

	end, err = util.ParseFlexibleDate("2025-01-05", now)
	if err != nil {
		t.Fatalf("ParseFlexibleDate(2025-01-05) error: %v", err)
	}
	if !end.Equal(day(2025, 1, 5)) {
		t.Errorf("ParseFlexibleDate(2025-01-05) = %v, want midnight 2025-01-05", end)
	}
	if got := simulate.DaysBetween(start, end); got != 4 {
		t.Errorf("DaysBetween with explicit midnight end: got %d, want 4", got)
	}
}

func TestResolveSeed(t *testing.T) {
	now := time.Date(2025, 1, 5, 14, 30, 0, 0, time.Local)

	// Unset -random-seed: time-based seed derived from now.
	fs := newRandomSeedFlags()
	if got := resolveSeed(fs, 99, now); got != now.UnixNano() {
		t.Errorf("resolveSeed (unset) = %d, want %d", got, now.UnixNano())
	}

	// Explicit -random-seed: returned verbatim, now ignored.
	fs = newRandomSeedFlags()
	if err := fs.Set("random-seed", "99"); err != nil {
		t.Fatal(err)
	}
	if got := resolveSeed(fs, 99, now); got != 99 {
		t.Errorf("resolveSeed (set) = %d, want 99", got)
	}
}
