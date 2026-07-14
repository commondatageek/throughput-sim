package util

import (
	"testing"
	"time"
)

// withLocal temporarily swaps time.Local so tests exercise the local-day helpers
// under a fixed non-UTC zone regardless of the machine's own timezone.
func withLocal(t *testing.T, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %q unavailable: %v", name, err)
	}
	orig := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = orig })
}

func TestParseDate_Local(t *testing.T) {
	withLocal(t, "America/New_York")

	got, err := ParseDate("2025-06-06")
	if err != nil {
		t.Fatalf("ParseDate error: %v", err)
	}
	want := time.Date(2025, 6, 6, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("ParseDate = %v, want local midnight %v", got, want)
	}
	if _, offset := got.Zone(); offset == 0 {
		t.Errorf("ParseDate produced a UTC offset; want the local (non-UTC) zone")
	}
}

func TestLocalDay_BucketsByLocalCalendarDay(t *testing.T) {
	withLocal(t, "America/New_York")

	// 23:30 local on 2025-06-06 (-04:00 in EDT) is 03:30 UTC on 2025-06-07.
	// UTC truncation would bucket it to the 7th; local bucketing keeps it on the 6th.
	completion := time.Date(2025, 6, 6, 23, 30, 0, 0, time.Local)
	got := LocalDay(completion)
	want := time.Date(2025, 6, 6, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("LocalDay(%v) = %v, want %v", completion, got, want)
	}

	// A UTC instant is converted into the local zone first.
	utcInstant := time.Date(2025, 6, 7, 3, 30, 0, 0, time.UTC) // == 23:30 -04:00 on the 6th
	if got := LocalDay(utcInstant); !got.Equal(want) {
		t.Errorf("LocalDay(%v UTC) = %v, want %v", utcInstant, got, want)
	}
}

func TestLocalDay_ZeroPassesThrough(t *testing.T) {
	if got := LocalDay(time.Time{}); !got.IsZero() {
		t.Errorf("LocalDay(zero) = %v, want zero", got)
	}
}

func TestDayIndex_DSTSpringForward(t *testing.T) {
	withLocal(t, "America/New_York")

	// DST spring-forward is 2025-03-09 (a 23-hour local day). A naive
	// int(sub.Hours()/24) would truncate the March 8→10 span to 1 day.
	start := time.Date(2025, 3, 8, 0, 0, 0, 0, time.Local)
	cases := []struct {
		day  time.Time
		want int
	}{
		{time.Date(2025, 3, 8, 0, 0, 0, 0, time.Local), 0},
		{time.Date(2025, 3, 9, 0, 0, 0, 0, time.Local), 1},  // the 23h day
		{time.Date(2025, 3, 10, 0, 0, 0, 0, time.Local), 2}, // would be 1 without rounding
		{time.Date(2025, 3, 7, 0, 0, 0, 0, time.Local), -1}, // negative offset
	}
	for _, c := range cases {
		if got := DayIndex(c.day, start); got != c.want {
			t.Errorf("DayIndex(%v, %v) = %d, want %d", c.day, start, got, c.want)
		}
	}
}

func TestDayIndex_DSTFallBack(t *testing.T) {
	withLocal(t, "America/New_York")

	// DST fall-back is 2025-11-02 (a 25-hour local day).
	start := time.Date(2025, 11, 1, 0, 0, 0, 0, time.Local)
	end := time.Date(2025, 11, 3, 0, 0, 0, 0, time.Local)
	if got := DayIndex(end, start); got != 2 {
		t.Errorf("DayIndex across fall-back = %d, want 2", got)
	}
}
