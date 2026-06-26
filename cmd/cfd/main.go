package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"math"
	"os"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

// normalizedIssue holds per-issue lifecycle event times clamped to be
// monotonically non-decreasing. All times are truncated to day resolution
// (midnight UTC). Zero means the event has not occurred.
type normalizedIssue struct {
	arrival     time.Time // created_at (zero → issue dropped)
	leftBacklog time.Time // started_at, or terminal time if canceled-before-start
	exit        time.Time // completed_at or canceled_at
	exitType    string    // "completed" | "canceled" | ""
}

func truncDay(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(24 * time.Hour)
}

// clampMin returns the later of a and floor; if a is zero it returns zero.
func clampMin(a, floor time.Time) time.Time {
	if a.IsZero() {
		return a
	}
	if a.Before(floor) {
		return floor
	}
	return a
}

func normalize(r sqlite.CFDRow) (normalizedIssue, bool) {
	arrival := truncDay(r.CreatedAt)
	if arrival.IsZero() {
		return normalizedIssue{}, false
	}

	completed := truncDay(r.CompletedAt)
	canceled := truncDay(r.CanceledAt)
	started := truncDay(r.StartedAt)

	// Determine the leftBacklog time.
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
	// If neither started nor terminal: still in backlog; leftBacklog stays zero.

	// Determine the exit time, clamped >= leftBacklog (or >= arrival if no leftBacklog).
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

	return normalizedIssue{
		arrival:     arrival,
		leftBacklog: leftBacklog,
		exit:        exit,
		exitType:    exitType,
	}, true
}

// dayRow holds the four cumulative line values and the three band heights for
// one calendar day.
type dayRow struct {
	Date        time.Time
	Created     int // cumulative arrivals ≤ Date
	LeftBacklog int // cumulative left-backlog ≤ Date
	Departed    int // cumulative exits (completed + canceled) ≤ Date
	Completed   int // cumulative completions ≤ Date
	// Band heights (derived).
	Backlog    int // Created - LeftBacklog
	InProgress int // LeftBacklog - Departed
	Canceled   int // Departed - Completed
	Done       int // Completed
}

func buildGrid(issues []normalizedIssue, start, end time.Time) []dayRow {
	var rows []dayRow
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		var created, leftBacklog, departed, completed int
		for _, ni := range issues {
			if !ni.arrival.After(d) {
				created++
			}
			if !ni.leftBacklog.IsZero() && !ni.leftBacklog.After(d) {
				leftBacklog++
			}
			if !ni.exit.IsZero() && !ni.exit.After(d) {
				departed++
				if ni.exitType == "completed" {
					completed++
				}
			}
		}
		rows = append(rows, dayRow{
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

// assertInvariants checks the four CFD invariants and returns a descriptive
// error on the first violation found.
func assertInvariants(rows []dayRow) error {
	for i, r := range rows {
		// Monotonic: each line must be >= the previous day's value.
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
		// Nested: Completed ≤ Departed ≤ LeftBacklog ≤ Created.
		if r.Completed > r.Departed {
			return fmt.Errorf("day %s: Completed (%d) > Departed (%d)", r.Date.Format("2006-01-02"), r.Completed, r.Departed)
		}
		if r.Departed > r.LeftBacklog {
			return fmt.Errorf("day %s: Departed (%d) > LeftBacklog (%d)", r.Date.Format("2006-01-02"), r.Departed, r.LeftBacklog)
		}
		if r.LeftBacklog > r.Created {
			return fmt.Errorf("day %s: LeftBacklog (%d) > Created (%d)", r.Date.Format("2006-01-02"), r.LeftBacklog, r.Created)
		}
		// Conservation: band heights sum to Created.
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

type flowHealth struct {
	ThroughputPerDay    float64 // slope of Completed line
	DeparturePerDay     float64 // slope of Departed line (completed + canceled)
	AvgWIPBacklog       float64
	AvgWIPInProgress    float64
	AvgWIPCanceled      float64
	AvgWIPDone          float64
	AvgCycleTimeDays    float64 // mean(completed_at - created_at) for issues completed in window
	LittlesLawCTDays    float64 // AvgWIPInProgress / ThroughputPerDay
	StabilityBacklog    float64 // slope of Backlog band height (items/day); ≈0 → stable
	StabilityInProgress float64
	StabilityCanceled   float64
	StabilityDone       float64
	WindowDays          int
	TotalIssues         int
	SkippedIssues       int
}

func computeHealth(rows []dayRow, issues []normalizedIssue, windowStart, windowEnd time.Time) flowHealth {
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

	// Avg cycle time: mean(exit - arrival) for issues that completed within the window.
	var cycleTimes []float64
	for _, ni := range issues {
		if ni.exitType == "completed" && !ni.exit.Before(windowStart) && !ni.exit.After(windowEnd) {
			days := ni.exit.Sub(ni.arrival).Hours() / 24
			if days >= 0 {
				cycleTimes = append(cycleTimes, days)
			}
		}
	}
	avgCT := mean(cycleTimes)

	// Little's Law estimate: avg WIP in-progress / throughput.
	avgWIPIP := mean(inProgressVals)
	var littlesCT float64
	if throughput > 0 {
		littlesCT = avgWIPIP / throughput
	}

	return flowHealth{
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

// --- JSON output ---

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

type jsonOutput struct {
	Series []jsonDayRow `json:"series"`
	Health struct {
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
	} `json:"flow_health"`
}

func outputJSON(rows []dayRow, h flowHealth) error {
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
	out.Health.ThroughputPerDay = math.Round(h.ThroughputPerDay*1000) / 1000
	out.Health.DeparturePerDay = math.Round(h.DeparturePerDay*1000) / 1000
	out.Health.AvgWIPBacklog = math.Round(h.AvgWIPBacklog*10) / 10
	out.Health.AvgWIPInProgress = math.Round(h.AvgWIPInProgress*10) / 10
	out.Health.AvgWIPCanceled = math.Round(h.AvgWIPCanceled*10) / 10
	out.Health.AvgCycleTimeDays = math.Round(h.AvgCycleTimeDays*10) / 10
	out.Health.LittlesLawCTDays = math.Round(h.LittlesLawCTDays*10) / 10
	out.Health.StabilityBacklog = math.Round(h.StabilityBacklog*1000) / 1000
	out.Health.StabilityInProgress = math.Round(h.StabilityInProgress*1000) / 1000
	out.Health.StabilityCanceled = math.Round(h.StabilityCanceled*1000) / 1000

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// --- HTML output ---

const htmlTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Cumulative Flow Diagram</title>
<script src="https://cdn.plot.ly/plotly-2.35.2.min.js"></script>
<style>
  *, *::before, *::after { box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 2rem auto; max-width: 1200px; color: #1a1a1a; background: #f5f5f5; padding: 0 1rem; }
  h1 { font-size: 1.35rem; font-weight: 600; margin: 0 0 0.25rem; }
  .meta { color: #555; font-size: 0.875rem; margin-bottom: 1.5rem; }
  #chart { background: #fff; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.10); margin-bottom: 1.5rem; }
  .health { background: #fff; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.10); padding: 1.25rem 1.5rem; margin-bottom: 1.5rem; }
  .health h2 { font-size: 1rem; font-weight: 600; margin: 0 0 1rem; }
  .stats-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 0.75rem 1.5rem; }
  .stat { }
  .stat-label { font-size: 0.75rem; color: #666; text-transform: uppercase; letter-spacing: 0.05em; }
  .stat-value { font-size: 1.1rem; font-weight: 600; font-variant-numeric: tabular-nums; margin-top: 0.1rem; }
  .stat-sub { font-size: 0.8rem; color: #555; margin-top: 0.1rem; }
  .band-table { border-collapse: collapse; width: 100%; margin-top: 1rem; font-size: 0.875rem; }
  .band-table th { text-align: left; padding: 0.35rem 0.75rem; font-size: 0.75rem; font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; color: #555; border-bottom: 1px solid #ddd; }
  .band-table td { padding: 0.35rem 0.75rem; border-bottom: 1px solid #f0f0f0; }
  .band-table tr:last-child td { border-bottom: none; }
  .verdict-stable { color: #15803d; }
  .verdict-widening { color: #b91c1c; }
  .verdict-narrowing { color: #c2410c; }
  .swatch { display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: 4px; vertical-align: middle; }
  .explainer { background: #fff; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.10); padding: 1.25rem 1.5rem; font-size: 0.875rem; line-height: 1.6; color: #444; }
  .explainer h2 { font-size: 1rem; font-weight: 600; margin: 0 0 0.5rem; color: #1a1a1a; }
</style>
</head>
<body>
<h1>Cumulative Flow Diagram</h1>
<p class="meta">{{.WindowStart}} → {{.WindowEnd}} &nbsp;·&nbsp; {{.TotalIssues}} issues{{if .SkippedIssues}} &nbsp;·&nbsp; {{.SkippedIssues}} skipped (no created_at){{end}}</p>

<div id="chart"></div>

<div class="health">
  <h2>Flow Health</h2>
  <div class="stats-grid">
    <div class="stat">
      <div class="stat-label">Throughput</div>
      <div class="stat-value">{{printf "%.2f" .ThroughputPerDay}} items/day</div>
      <div class="stat-sub">slope of Done line</div>
    </div>
    <div class="stat">
      <div class="stat-label">Total departure rate</div>
      <div class="stat-value">{{printf "%.2f" .DeparturePerDay}} items/day</div>
      <div class="stat-sub">completed + canceled</div>
    </div>
    <div class="stat">
      <div class="stat-label">Avg cycle time</div>
      <div class="stat-value">{{printf "%.1f" .AvgCycleTimeDays}} days</div>
      <div class="stat-sub">created→completed (in window)</div>
    </div>
    <div class="stat">
      <div class="stat-label">Little's Law cycle time</div>
      <div class="stat-value">{{printf "%.1f" .LittlesLawCTDays}} days</div>
      <div class="stat-sub">avg WIP in-progress ÷ throughput</div>
    </div>
  </div>

  <table class="band-table" style="margin-top:1.25rem">
    <thead>
      <tr><th>Band</th><th>Avg WIP</th><th>Stability</th><th>Verdict</th></tr>
    </thead>
    <tbody>
      <tr>
        <td><span class="swatch" style="background:#636EFA"></span>Backlog</td>
        <td>{{printf "%.1f" .AvgWIPBacklog}}</td>
        <td>{{printf "%+.3f" .StabilityBacklog}} items/day</td>
        <td class="{{.VerdictClassBacklog}}">{{.VerdictBacklog}}</td>
      </tr>
      <tr>
        <td><span class="swatch" style="background:#00CC96"></span>In Progress</td>
        <td>{{printf "%.1f" .AvgWIPInProgress}}</td>
        <td>{{printf "%+.3f" .StabilityInProgress}} items/day</td>
        <td class="{{.VerdictClassInProgress}}">{{.VerdictInProgress}}</td>
      </tr>
      <tr>
        <td><span class="swatch" style="background:#EF553B"></span>Canceled</td>
        <td>{{printf "%.1f" .AvgWIPCanceled}}</td>
        <td>{{printf "%+.3f" .StabilityCanceled}} items/day</td>
        <td class="{{.VerdictClassCanceled}}">{{.VerdictCanceled}}</td>
      </tr>
      <tr>
        <td><span class="swatch" style="background:#AB63FA"></span>Done</td>
        <td>{{printf "%.1f" .AvgWIPDone}}</td>
        <td>&mdash;</td>
        <td>&mdash;</td>
      </tr>
    </tbody>
  </table>
</div>

<div class="explainer">
  <h2>How to read this diagram</h2>
  <p>Each colored band represents items currently in that stage. The band's <strong>vertical height</strong> at any point equals the WIP in that stage. The <strong>horizontal distance</strong> between the top edge of a band and its bottom edge approximates the average time items spend there (Little's Law: WIP = throughput × cycle time).</p>
  <p><strong>Ideal shape</strong>: straight, parallel lines with constant-width bands. Straight means a steady rate; parallel means inflow equals outflow at every boundary so no queue is accumulating. A <em>widening</em> band signals a bottleneck; a <em>narrowing</em> band means that stage is draining. A flattening top line means fewer new issues are being created.</p>
</div>

<script>
var dates = {{.DatesJSON}};
var traces = [
  {
    name: 'Done',
    x: dates, y: {{.DoneJSON}},
    stackgroup: 'one',
    fillcolor: 'rgba(171,99,250,0.7)',
    line: {color: '#AB63FA', width: 1},
    mode: 'lines',
    hovertemplate: '%{y} done<extra>Done</extra>'
  },
  {
    name: 'Canceled',
    x: dates, y: {{.CanceledJSON}},
    stackgroup: 'one',
    fillcolor: 'rgba(239,85,59,0.7)',
    line: {color: '#EF553B', width: 1},
    mode: 'lines',
    hovertemplate: '%{y} canceled<extra>Canceled</extra>'
  },
  {
    name: 'In Progress',
    x: dates, y: {{.InProgressJSON}},
    stackgroup: 'one',
    fillcolor: 'rgba(0,204,150,0.7)',
    line: {color: '#00CC96', width: 1},
    mode: 'lines',
    hovertemplate: '%{y} in progress<extra>In Progress</extra>'
  },
  {
    name: 'Backlog',
    x: dates, y: {{.BacklogJSON}},
    stackgroup: 'one',
    fillcolor: 'rgba(99,110,250,0.7)',
    line: {color: '#636EFA', width: 1},
    mode: 'lines',
    hovertemplate: '%{y} in backlog<extra>Backlog</extra>'
  }
];

var layout = {
  hovermode: 'x unified',
  xaxis: {type: 'date', title: ''},
  yaxis: {title: 'Cumulative issues'},
  legend: {traceorder: 'reversed'},
  margin: {t: 20, r: 20, b: 40, l: 60},
  height: 480,
  paper_bgcolor: '#fff',
  plot_bgcolor: '#fff'
};

Plotly.newPlot('chart', traces, layout, {responsive: true, displayModeBar: false});
</script>
</body>
</html>`

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

func outputHTML(rows []dayRow, h flowHealth, totalIssues, skippedIssues int, windowStart, windowEnd time.Time, out *os.File) error {
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

	tmpl := template.Must(template.New("cfd").Parse(htmlTmpl))
	return tmpl.Execute(out, data)
}

func main() {
	dbFile := flag.String("db", "", "path to SQLite database")
	startStr := flag.String("start", "", "Start date, inclusive (YYYY-MM-DD; default: today minus 3 months)")
	endStr := flag.String("end", "", "End date, inclusive (YYYY-MM-DD; default: today)")
	format := flag.String("format", "html", "Output format: html, json")
	outPath := flag.String("out", "", "Write output to this file instead of stdout")
	var teams linear.KeyList
	flag.Var(&teams, "teams", "Comma-separated team keys to filter by (e.g. ENG,DATA); default: all teams")
	flag.Parse()

	if *dbFile == "" {
		fmt.Fprintln(os.Stderr, "error: -db is required")
		os.Exit(1)
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)

	windowEnd := today
	if *endStr != "" {
		t, err := util.ParseDate(*endStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -end %q: %v\n", *endStr, err)
			os.Exit(1)
		}
		windowEnd = t
	}

	windowStart := today.AddDate(0, -3, 0)
	if *startStr != "" {
		t, err := util.ParseDate(*startStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -start %q: %v\n", *startStr, err)
			os.Exit(1)
		}
		windowStart = t
	}

	if !windowStart.Before(windowEnd) {
		fmt.Fprintln(os.Stderr, "error: -start must be before -end")
		os.Exit(1)
	}

	store, err := sqlite.Open(*dbFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	raw, err := store.CFDIssues(context.Background(), teams)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: query issues: %v\n", err)
		os.Exit(1)
	}

	var normalized []normalizedIssue
	skipped := 0
	for _, r := range raw {
		ni, ok := normalize(r)
		if !ok {
			skipped++
			continue
		}
		normalized = append(normalized, ni)
	}

	rows := buildGrid(normalized, windowStart, windowEnd)

	if err := assertInvariants(rows); err != nil {
		fmt.Fprintf(os.Stderr, "error: CFD invariant violated: %v\n", err)
		os.Exit(1)
	}

	health := computeHealth(rows, normalized, windowStart, windowEnd)
	health.TotalIssues = len(raw)
	health.SkippedIssues = skipped

	out := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: create output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	switch *format {
	case "html":
		if err := outputHTML(rows, health, len(raw), skipped, windowStart, windowEnd, out); err != nil {
			fmt.Fprintf(os.Stderr, "error: render HTML: %v\n", err)
			os.Exit(1)
		}
	case "json":
		if err := outputJSON(rows, health); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode JSON: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "error: unknown -format %q (use html or json)\n", *format)
		os.Exit(1)
	}
}
