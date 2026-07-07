package cfd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
)

//go:embed template.html
var htmlTemplate string

// Options controls which issues are included and the CFD's display window.
// Every field mirrors a `forecast cfd` flag (and thus a `-config` YAML key of
// the same name).
type Options struct {
	// Teams is the `-teams` flag: team keys to filter by; empty means all teams.
	Teams linear.KeyList
	// Start is the `-start` flag, resolved to a concrete date (default: today
	// minus 3 months).
	Start time.Time
	// End is the `-end` flag, resolved to a concrete date (default: today).
	End time.Time
}

// NormalizedIssue holds per-issue lifecycle event times clamped to be
// monotonically non-decreasing. All times are truncated to day resolution
// (midnight UTC). Zero means the event has not occurred.
type NormalizedIssue struct {
	Arrival     time.Time
	LeftBacklog time.Time
	Exit        time.Time
	ExitType    string // "completed" | "canceled" | ""
}

// DayRow holds the four cumulative line values and the three band heights for
// one calendar day.
type DayRow struct {
	Date        time.Time
	Created     int
	LeftBacklog int
	Departed    int
	Completed   int
	Backlog     int
	InProgress  int
	Canceled    int
	Done        int
}

// FlowHealth holds computed flow metrics for a CFD window.
type FlowHealth struct {
	ThroughputPerDay    float64
	DeparturePerDay     float64
	AvgWIPBacklog       float64
	AvgWIPInProgress    float64
	AvgWIPCanceled      float64
	AvgWIPDone          float64
	AvgCycleTimeDays    float64
	LittlesLawCTDays    float64
	StabilityBacklog    float64
	StabilityInProgress float64
	StabilityCanceled   float64
	StabilityDone       float64
	WindowDays          int
	TotalIssues         int
	SkippedIssues       int
}

func truncDay(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(24 * time.Hour)
}

func clampMin(a, floor time.Time) time.Time {
	if a.IsZero() {
		return a
	}
	if a.Before(floor) {
		return floor
	}
	return a
}

// Normalize converts a CFDRow into a NormalizedIssue with monotonically
// non-decreasing timestamps. Returns false if the issue has no created_at and
// should be dropped.
func Normalize(r sqlite.CFDRow) (NormalizedIssue, bool) {
	arrival := truncDay(r.CreatedAt)
	if arrival.IsZero() {
		return NormalizedIssue{}, false
	}

	completed := truncDay(r.CompletedAt)
	canceled := truncDay(r.CanceledAt)
	started := truncDay(r.StartedAt)

	var leftBacklog time.Time
	if !started.IsZero() {
		leftBacklog = clampMin(started, arrival)
	} else if !completed.IsZero() || !canceled.IsZero() {
		// Canceled or completed without ever having started_at set.
		// Use the terminal time so the issue exits the backlog at the moment it exits the system.
		terminal := completed
		if terminal.IsZero() {
			terminal = canceled
		}
		leftBacklog = clampMin(terminal, arrival)
	}

	floor := leftBacklog
	if floor.IsZero() {
		floor = arrival
	}

	var exit time.Time
	var exitType string
	switch {
	case !completed.IsZero():
		exit = clampMin(completed, floor)
		exitType = "completed"
	case !canceled.IsZero():
		exit = clampMin(canceled, floor)
		exitType = "canceled"
	}

	return NormalizedIssue{
		Arrival:     arrival,
		LeftBacklog: leftBacklog,
		Exit:        exit,
		ExitType:    exitType,
	}, true
}

// BuildGrid computes cumulative line values for each calendar day in [start, end].
func BuildGrid(issues []NormalizedIssue, start, end time.Time) []DayRow {
	var rows []DayRow
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		var created, leftBacklog, departed, completed int
		for _, ni := range issues {
			if !ni.Arrival.After(d) {
				created++
			}
			if !ni.LeftBacklog.IsZero() && !ni.LeftBacklog.After(d) {
				leftBacklog++
			}
			if !ni.Exit.IsZero() && !ni.Exit.After(d) {
				departed++
				if ni.ExitType == "completed" {
					completed++
				}
			}
		}
		rows = append(rows, DayRow{
			Date:        d,
			Created:     created,
			LeftBacklog: leftBacklog,
			Departed:    departed,
			Completed:   completed,
			Backlog:     created - leftBacklog,
			InProgress:  leftBacklog - departed,
			Canceled:    departed - completed,
			Done:        completed,
		})
	}
	return rows
}

// AssertInvariants checks the four CFD invariants and returns a descriptive
// error on the first violation found.
func AssertInvariants(rows []DayRow) error {
	for i, r := range rows {
		if i > 0 {
			p := rows[i-1]
			if r.Created < p.Created {
				return fmt.Errorf("day %s: Created not monotonic (%d < %d)", r.Date.Format("2006-01-02"), r.Created, p.Created)
			}
			if r.LeftBacklog < p.LeftBacklog {
				return fmt.Errorf("day %s: LeftBacklog not monotonic (%d < %d)", r.Date.Format("2006-01-02"), r.LeftBacklog, p.LeftBacklog)
			}
			if r.Departed < p.Departed {
				return fmt.Errorf("day %s: Departed not monotonic (%d < %d)", r.Date.Format("2006-01-02"), r.Departed, p.Departed)
			}
			if r.Completed < p.Completed {
				return fmt.Errorf("day %s: Completed not monotonic (%d < %d)", r.Date.Format("2006-01-02"), r.Completed, p.Completed)
			}
		}
		if r.Completed > r.Departed {
			return fmt.Errorf("day %s: Completed (%d) > Departed (%d)", r.Date.Format("2006-01-02"), r.Completed, r.Departed)
		}
		if r.Departed > r.LeftBacklog {
			return fmt.Errorf("day %s: Departed (%d) > LeftBacklog (%d)", r.Date.Format("2006-01-02"), r.Departed, r.LeftBacklog)
		}
		if r.LeftBacklog > r.Created {
			return fmt.Errorf("day %s: LeftBacklog (%d) > Created (%d)", r.Date.Format("2006-01-02"), r.LeftBacklog, r.Created)
		}
		bandSum := r.Done + r.Canceled + r.InProgress + r.Backlog
		if bandSum != r.Created {
			return fmt.Errorf("day %s: band sum %d != Created %d", r.Date.Format("2006-01-02"), bandSum, r.Created)
		}
	}
	return nil
}

// linearSlope returns the least-squares slope of a series of y values at
// integer x positions 0, 1, …, n-1. Units are (y-unit) per day.
func linearSlope(ys []float64) float64 {
	n := len(ys)
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i, y := range ys {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	denom := float64(n)*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (float64(n)*sumXY - sumX*sumY) / denom
}

func mean(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vs {
		sum += v
	}
	return sum / float64(len(vs))
}

// ComputeHealth derives flow-health statistics from the CFD grid.
func ComputeHealth(rows []DayRow, issues []NormalizedIssue, windowStart, windowEnd time.Time) FlowHealth {
	n := len(rows)

	completedVals := make([]float64, n)
	departedVals := make([]float64, n)
	backlogVals := make([]float64, n)
	inProgressVals := make([]float64, n)
	canceledVals := make([]float64, n)
	doneVals := make([]float64, n)

	for i, r := range rows {
		completedVals[i] = float64(r.Completed)
		departedVals[i] = float64(r.Departed)
		backlogVals[i] = float64(r.Backlog)
		inProgressVals[i] = float64(r.InProgress)
		canceledVals[i] = float64(r.Canceled)
		doneVals[i] = float64(r.Done)
	}

	throughput := linearSlope(completedVals)
	departure := linearSlope(departedVals)

	var cycleTimes []float64
	for _, ni := range issues {
		if ni.ExitType == "completed" && !ni.Exit.Before(windowStart) && !ni.Exit.After(windowEnd) {
			days := ni.Exit.Sub(ni.Arrival).Hours() / 24
			if days >= 0 {
				cycleTimes = append(cycleTimes, days)
			}
		}
	}
	avgCT := mean(cycleTimes)

	avgWIPIP := mean(inProgressVals)
	var littlesCT float64
	if throughput > 0 {
		littlesCT = avgWIPIP / throughput
	}

	return FlowHealth{
		ThroughputPerDay:    throughput,
		DeparturePerDay:     departure,
		AvgWIPBacklog:       mean(backlogVals),
		AvgWIPInProgress:    avgWIPIP,
		AvgWIPCanceled:      mean(canceledVals),
		AvgWIPDone:          mean(doneVals),
		AvgCycleTimeDays:    avgCT,
		LittlesLawCTDays:    littlesCT,
		StabilityBacklog:    linearSlope(backlogVals),
		StabilityInProgress: linearSlope(inProgressVals),
		StabilityCanceled:   linearSlope(canceledVals),
		StabilityDone:       linearSlope(doneVals),
		WindowDays:          n,
	}
}

func stabilityVerdict(slope float64) string {
	switch {
	case math.Abs(slope) < 0.05:
		return "stable"
	case slope > 0:
		return "widening (bottleneck)"
	default:
		return "narrowing (draining)"
	}
}

func verdictClass(v string) string {
	switch v {
	case "stable":
		return "verdict-stable"
	case "widening (bottleneck)":
		return "verdict-widening"
	default:
		return "verdict-narrowing"
	}
}

func mustJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return template.JS(b)
}

// RenderJSON writes the CFD grid and health stats as JSON to w.
func RenderJSON(w io.Writer, rows []DayRow, h FlowHealth) error {
	type jsonDayRow struct {
		Date        string `json:"date"`
		Created     int    `json:"created"`
		LeftBacklog int    `json:"left_backlog"`
		Departed    int    `json:"departed"`
		Completed   int    `json:"completed"`
		Backlog     int    `json:"backlog"`
		InProgress  int    `json:"in_progress"`
		Canceled    int    `json:"canceled"`
		Done        int    `json:"done"`
	}
	type jsonHealth struct {
		ThroughputPerDay    float64 `json:"throughput_per_day"`
		DeparturePerDay     float64 `json:"departure_per_day"`
		AvgWIPBacklog       float64 `json:"avg_wip_backlog"`
		AvgWIPInProgress    float64 `json:"avg_wip_in_progress"`
		AvgWIPCanceled      float64 `json:"avg_wip_canceled"`
		AvgCycleTimeDays    float64 `json:"avg_cycle_time_days"`
		LittlesLawCTDays    float64 `json:"littles_law_ct_days"`
		StabilityBacklog    float64 `json:"stability_backlog_slope"`
		StabilityInProgress float64 `json:"stability_in_progress_slope"`
		StabilityCanceled   float64 `json:"stability_canceled_slope"`
	}
	type jsonOutput struct {
		Series []jsonDayRow `json:"series"`
		Health jsonHealth   `json:"flow_health"`
	}

	var out jsonOutput
	for _, r := range rows {
		out.Series = append(out.Series, jsonDayRow{
			Date:        r.Date.Format("2006-01-02"),
			Created:     r.Created,
			LeftBacklog: r.LeftBacklog,
			Departed:    r.Departed,
			Completed:   r.Completed,
			Backlog:     r.Backlog,
			InProgress:  r.InProgress,
			Canceled:    r.Canceled,
			Done:        r.Done,
		})
	}
	out.Health = jsonHealth{
		ThroughputPerDay:    math.Round(h.ThroughputPerDay*1000) / 1000,
		DeparturePerDay:     math.Round(h.DeparturePerDay*1000) / 1000,
		AvgWIPBacklog:       math.Round(h.AvgWIPBacklog*10) / 10,
		AvgWIPInProgress:    math.Round(h.AvgWIPInProgress*10) / 10,
		AvgWIPCanceled:      math.Round(h.AvgWIPCanceled*10) / 10,
		AvgCycleTimeDays:    math.Round(h.AvgCycleTimeDays*10) / 10,
		LittlesLawCTDays:    math.Round(h.LittlesLawCTDays*10) / 10,
		StabilityBacklog:    math.Round(h.StabilityBacklog*1000) / 1000,
		StabilityInProgress: math.Round(h.StabilityInProgress*1000) / 1000,
		StabilityCanceled:   math.Round(h.StabilityCanceled*1000) / 1000,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

type htmlData struct {
	WindowStart            string
	WindowEnd              string
	TotalIssues            int
	SkippedIssues          int
	ThroughputPerDay       float64
	DeparturePerDay        float64
	AvgWIPBacklog          float64
	AvgWIPInProgress       float64
	AvgWIPCanceled         float64
	AvgWIPDone             float64
	AvgCycleTimeDays       float64
	LittlesLawCTDays       float64
	StabilityBacklog       float64
	StabilityInProgress    float64
	StabilityCanceled      float64
	VerdictBacklog         string
	VerdictClassBacklog    string
	VerdictInProgress      string
	VerdictClassInProgress string
	VerdictCanceled        string
	VerdictClassCanceled   string
	DatesJSON              template.JS
	DoneJSON               template.JS
	CanceledJSON           template.JS
	InProgressJSON         template.JS
	BacklogJSON            template.JS
}

// RenderHTML writes an interactive Plotly CFD chart as HTML to w.
func RenderHTML(w io.Writer, rows []DayRow, h FlowHealth, totalIssues, skippedIssues int, windowStart, windowEnd time.Time) error {
	dates := make([]string, len(rows))
	done := make([]int, len(rows))
	canceled := make([]int, len(rows))
	inProgress := make([]int, len(rows))
	backlog := make([]int, len(rows))

	for i, r := range rows {
		dates[i] = r.Date.Format("2006-01-02")
		done[i] = r.Done
		canceled[i] = r.Canceled
		inProgress[i] = r.InProgress
		backlog[i] = r.Backlog
	}

	vBacklog := stabilityVerdict(h.StabilityBacklog)
	vIP := stabilityVerdict(h.StabilityInProgress)
	vCanceled := stabilityVerdict(h.StabilityCanceled)

	data := htmlData{
		WindowStart:            windowStart.Format("2006-01-02"),
		WindowEnd:              windowEnd.Format("2006-01-02"),
		TotalIssues:            totalIssues,
		SkippedIssues:          skippedIssues,
		ThroughputPerDay:       h.ThroughputPerDay,
		DeparturePerDay:        h.DeparturePerDay,
		AvgWIPBacklog:          h.AvgWIPBacklog,
		AvgWIPInProgress:       h.AvgWIPInProgress,
		AvgWIPCanceled:         h.AvgWIPCanceled,
		AvgWIPDone:             h.AvgWIPDone,
		AvgCycleTimeDays:       h.AvgCycleTimeDays,
		LittlesLawCTDays:       h.LittlesLawCTDays,
		StabilityBacklog:       h.StabilityBacklog,
		StabilityInProgress:    h.StabilityInProgress,
		StabilityCanceled:      h.StabilityCanceled,
		VerdictBacklog:         vBacklog,
		VerdictClassBacklog:    verdictClass(vBacklog),
		VerdictInProgress:      vIP,
		VerdictClassInProgress: verdictClass(vIP),
		VerdictCanceled:        vCanceled,
		VerdictClassCanceled:   verdictClass(vCanceled),
		DatesJSON:              mustJSON(dates),
		DoneJSON:               mustJSON(done),
		CanceledJSON:           mustJSON(canceled),
		InProgressJSON:         mustJSON(inProgress),
		BacklogJSON:            mustJSON(backlog),
	}

	tmpl := template.Must(template.New("cfd").Parse(htmlTemplate))
	return tmpl.Execute(w, data)
}
