package simulate

import (
	"time"

	"github.com/commondatageek/delivery-forecast/internal/util"
)

// BacktestItem is the neutral per-issue record the backtest needs: just the
// timestamps. The cmd layer converts source-specific records (e.g. linear.Issue)
// to BacktestItem before calling RunBacktest.
type BacktestItem struct {
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

// BacktestRow is one day's entry in the backtest output.
type BacktestRow struct {
	Date      time.Time
	Completed int
	Remaining int
	Prob      float64
}

// CountAsOf counts how many items in the fixed set were completed by midnight
// of d, and how many had been created by that point but were not yet complete.
func CountAsOf(items []BacktestItem, d time.Time) (completed, remaining int) {
	for _, it := range items {
		completedByD := !it.CompletedAt.IsZero() && !it.CompletedAt.After(d)
		notYetCreated := !it.CreatedAt.IsZero() && it.CreatedAt.After(d)
		switch {
		case completedByD:
			completed++
		case notYetCreated:
			// neither column
		default:
			remaining++
		}
	}
	return
}

// EarliestStartedAt returns the minimum non-zero StartedAt across all items,
// or the zero time if none have one.
func EarliestStartedAt(items []BacktestItem) time.Time {
	var earliest time.Time
	for _, it := range items {
		if it.StartedAt.IsZero() {
			continue
		}
		if earliest.IsZero() || it.StartedAt.Before(earliest) {
			earliest = it.StartedAt
		}
	}
	return earliest
}

// AllCreatedBy reports whether every item had been created by d.
func AllCreatedBy(items []BacktestItem, d time.Time) bool {
	for _, it := range items {
		if !it.CreatedAt.IsZero() && it.CreatedAt.After(d) {
			return false
		}
	}
	return true
}

// RunBacktest replays probability forecasts day-by-day from startDate through
// targetDate (inclusive) using the fixed items set and sample pool. On each day
// it counts completed/remaining items and runs a Monte Carlo forecast for the
// remaining window. The loop exits early once all items are complete and have
// been created.
func RunBacktest(pool *SamplePool, items []BacktestItem, startDate, targetDate time.Time, p Params) []BacktestRow {
	var rows []BacktestRow
	for d := startDate; !d.After(targetDate); d = d.AddDate(0, 0, 1) {
		completed, remaining := CountAsOf(items, d)
		daysToTarget := util.DayIndex(targetDate, d) + 1

		var prob float64
		if remaining == 0 {
			prob = 100.0
		} else {
			dist := ItemsInDays(pool, Params{
				Mode:        p.Mode,
				Team:        p.Team,
				Engineers:   p.Engineers,
				Days:        daysToTarget,
				Simulations: p.Simulations,
				Workers:     p.Workers,
				Seed:        p.Seed,
			})
			prob = ProbabilityAtLeast(dist, remaining)
		}
		rows = append(rows, BacktestRow{d, completed, remaining, prob})

		if remaining == 0 && AllCreatedBy(items, d) {
			break
		}
	}
	return rows
}
