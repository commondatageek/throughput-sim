package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"math"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
	"forecasting/internal/util"
)

type reportItem struct {
	Identifier  string
	Title       string
	Assignee    string
	ProjectName string
	StateType   string
	StateName   string
	StartedAt   time.Time
	AgeDays     float64
	Percentile  int
}

// p85value returns the 85th-percentile value from a sorted slice.
func p85value(sorted []float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(0.85 * float64(len(sorted)-1)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func formatStartDate(t time.Time) string {
	return t.Local().Format("Mon 1/2")
}

// formatState renders the workflow state for display, pairing Linear's
// human-readable state name with its underlying type, e.g. "In Review (started)".
// Falls back gracefully when either piece is missing.
func formatState(name, typ string) string {
	switch {
	case name != "" && typ != "":
		return fmt.Sprintf("%s (%s)", name, typ)
	case name != "":
		return name
	default:
		return typ
	}
}

// loadFromDB reads cycle times and in-progress items from the SQLite store.
func loadFromDB(dbPath string, sampleStart, sampleEnd time.Time, minCycleTime time.Duration, today time.Time, teamKeys []string) (cycleTimes []float64, inProgress []reportItem, err error) {
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	completed, err := store.CompletedBetween(context.Background(), sampleStart, sampleEnd, nil, teamKeys)
	if err != nil {
		return nil, nil, fmt.Errorf("query completed: %w", err)
	}
	for _, it := range completed {
		if it.StartedAt.IsZero() || it.CompletedAt.IsZero() {
			continue
		}
		cycleTime := it.CompletedAt.Sub(it.StartedAt)
		if cycleTime < minCycleTime {
			continue
		}
		days := cycleTime.Hours() / 24
		if days >= 0 {
			cycleTimes = append(cycleTimes, days)
		}
	}

	active, err := store.InProgress(context.Background(), teamKeys)
	if err != nil {
		return nil, nil, fmt.Errorf("query in-progress: %w", err)
	}
	for _, it := range active {
		ageDays := today.Sub(it.StartedAt).Hours() / 24
		if ageDays < 0 {
			ageDays = 0
		}
		inProgress = append(inProgress, reportItem{
			Identifier:  it.Identifier,
			Title:       it.Title,
			Assignee:    it.Assignee,
			ProjectName: it.ProjectName,
			StateType:   it.StateType,
			StateName:   it.StateName,
			StartedAt:   it.StartedAt,
			AgeDays:     ageDays,
		})
	}

	return cycleTimes, inProgress, nil
}

func main() {
	dbFile := flag.String("db", "", "path to SQLite database")
	sampleStartStr := flag.String("sample-start", "", "Start of completed-issue window (YYYY-MM-DD, default: today minus 3 months)")
	sampleEndStr := flag.String("sample-end", "", "End of completed-issue window (YYYY-MM-DD, default: today)")
	format := flag.String("format", "text", "Output format: text, json, html")
	minCycleTimeStr := flag.String("min-cycle-time", "", "Exclude completed issues with cycle time below this duration from the percentile distribution (e.g. 5m, 1h, 1d)")
	var teams linear.KeyList
	flag.Var(&teams, "teams", "Comma-separated team keys to filter by (e.g. DATA,PLT); default: all teams")
	flag.Parse()

	if *dbFile == "" {
		fmt.Fprintln(os.Stderr, "error: -db is required")
		os.Exit(1)
	}

	var minCycleTime time.Duration
	if *minCycleTimeStr != "" {
		d, err := util.ParseFlexibleDuration(*minCycleTimeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -min-cycle-time %q: %v\n", *minCycleTimeStr, err)
			os.Exit(1)
		}
		minCycleTime = d
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)

	sampleEnd := today
	if *sampleEndStr != "" {
		t, err := util.ParseDate(*sampleEndStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -sample-end %q: %v\n", *sampleEndStr, err)
			os.Exit(1)
		}
		sampleEnd = t
	}

	sampleStart := today.AddDate(0, -3, 0)
	if *sampleStartStr != "" {
		t, err := util.ParseDate(*sampleStartStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -sample-start %q: %v\n", *sampleStartStr, err)
			os.Exit(1)
		}
		sampleStart = t
	}

	if !sampleStart.Before(sampleEnd) {
		fmt.Fprintln(os.Stderr, "error: -sample-start must be before -sample-end")
		os.Exit(1)
	}

	cycleTimes, inProgress, err := loadFromDB(*dbFile, sampleStart, sampleEnd, minCycleTime, today, teams)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	sort.Float64s(cycleTimes)

	for i := range inProgress {
		inProgress[i].Percentile = util.ComputePercentile(cycleTimes, inProgress[i].AgeDays)
	}

	sort.Slice(inProgress, func(i, j int) bool {
		return inProgress[i].AgeDays > inProgress[j].AgeDays
	})

	p85 := p85value(cycleTimes)

	if len(cycleTimes) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no completed issues found in the sample window; percentiles will be 0")
	}

	switch *format {
	case "text":
		outputText(inProgress, cycleTimes, p85, sampleStart, sampleEnd)
	case "json":
		outputJSON(inProgress)
	case "html":
		outputHTML(inProgress, p85, sampleStart, sampleEnd, len(cycleTimes))
	default:
		fmt.Fprintf(os.Stderr, "error: unknown -format %q (use text, json, or html)\n", *format)
		os.Exit(1)
	}
}

func outputText(items []reportItem, cycleTimes []float64, p85 float64, sampleStart, sampleEnd time.Time) {
	fmt.Printf("Cycle time distribution: %d completed issues (%s to %s)  ·  P85: %.1f days\n\n",
		len(cycleTimes),
		sampleStart.Format("2006-01-02"),
		sampleEnd.Format("2006-01-02"),
		p85,
	)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "IDENTIFIER\tTITLE\tDAYS\tPERCENTILE\tSTATE\tSTART DATE\tASSIGNEE")
	for _, item := range items {
		pct := item.Percentile
		fmt.Fprintf(w, "%s\t%s\t%.1f\t%d%s\t%s\t%s\t%s\n",
			item.Identifier,
			truncateTitle(item.Title),
			item.AgeDays,
			pct, util.OrdinalSuffix(pct),
			formatState(item.StateName, item.StateType),
			formatStartDate(item.StartedAt),
			item.Assignee,
		)
	}
	w.Flush()
}

func truncateTitle(title string) string {
	const maxLen = 50
	if len(title) <= maxLen {
		return title
	}
	return title[:maxLen] + "..."
}

type jsonItem struct {
	Identifier  string  `json:"identifier"`
	Title       string  `json:"title"`
	Assignee    string  `json:"assignee"`
	ProjectName string  `json:"project_name,omitempty"`
	StateType   string  `json:"state_type,omitempty"`
	StateName   string  `json:"state_name,omitempty"`
	StartedAt   string  `json:"started_at"`
	AgeDays     float64 `json:"age_days"`
	Percentile  int     `json:"percentile"`
}

func outputJSON(items []reportItem) {
	out := make([]jsonItem, len(items))
	for i, item := range items {
		out[i] = jsonItem{
			Identifier:  item.Identifier,
			Title:       item.Title,
			Assignee:    item.Assignee,
			ProjectName: item.ProjectName,
			StateType:   item.StateType,
			StateName:   item.StateName,
			StartedAt:   item.StartedAt.Format("2006-01-02"),
			AgeDays:     math.Round(item.AgeDays*10) / 10,
			Percentile:  item.Percentile,
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error: encode JSON: %v\n", err)
		os.Exit(1)
	}
}

const htmlTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>In-Progress Issue Age Report</title>
<style>
  *, *::before, *::after { box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 2rem auto; max-width: 1100px; color: #1a1a1a; background: #f5f5f5; }
  h1 { font-size: 1.35rem; font-weight: 600; margin: 0 0 0.4rem; }
  .meta { color: #555; font-size: 0.875rem; margin-bottom: 1.75rem; }
  .meta strong { color: #1a1a1a; }
  table { border-collapse: collapse; width: 100%; background: #fff; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.10); }
  th { background: #f0f0f0; text-align: left; padding: 0.6rem 1rem; font-size: 0.75rem; font-weight: 600; letter-spacing: 0.05em; text-transform: uppercase; color: #555; border-bottom: 1px solid #ddd; }
  td { padding: 0.6rem 1rem; border-bottom: 1px solid #eee; font-size: 0.875rem; vertical-align: middle; }
  tr:last-child td { border-bottom: none; }
  tr:hover td { background: #fafafa; }
  .num { font-variant-numeric: tabular-nums; }
  .high { color: #b91c1c; font-weight: 600; }
  .medium { color: #c2410c; }
  .normal { color: #1a1a1a; }
</style>
</head>
<body>
<h1>In-Progress Issue Age Report</h1>
<p class="meta">
  <strong>{{.Count}}</strong> in-progress issues &nbsp;·&nbsp;
  P85 cycle time: <strong>{{printf "%.1f" .P85}} days</strong> &nbsp;·&nbsp;
  Distribution: {{.CompletedCount}} completed issues from {{.SampleStart}} to {{.SampleEnd}}
</p>
<table>
  <thead>
    <tr>
      <th>Identifier</th>
      <th>Title</th>
      <th>Days</th>
      <th>Percentile</th>
      <th>State</th>
      <th>Start Date</th>
      <th>Assignee</th>
    </tr>
  </thead>
  <tbody>
    {{range .Items}}
    <tr>
      <td>{{.Identifier}}</td>
      <td>{{.Title}}</td>
      <td class="num {{.AgeClass}}">{{printf "%.1f" .AgeDays}}</td>
      <td class="num {{.AgeClass}}">{{.Percentile}}{{.Suffix}}</td>
      <td>{{.State}}</td>
      <td>{{.StartDate}}</td>
      <td>{{.Assignee}}</td>
    </tr>
    {{end}}
  </tbody>
</table>
</body>
</html>`

type htmlItem struct {
	Identifier string
	Title      string
	AgeDays    float64
	Percentile int
	Suffix     string
	AgeClass   string
	State      string
	StartDate  string
	Assignee   string
}

type htmlData struct {
	Count          int
	CompletedCount int
	P85            float64
	SampleStart    string
	SampleEnd      string
	Items          []htmlItem
}

func ageClass(pct int) string {
	switch {
	case pct >= 85:
		return "high"
	case pct >= 70:
		return "medium"
	default:
		return "normal"
	}
}

func outputHTML(items []reportItem, p85 float64, sampleStart, sampleEnd time.Time, completedCount int) {
	tmpl := template.Must(template.New("report").Parse(htmlTmpl))
	data := htmlData{
		Count:          len(items),
		CompletedCount: completedCount,
		P85:            p85,
		SampleStart:    sampleStart.Format("2006-01-02"),
		SampleEnd:      sampleEnd.Format("2006-01-02"),
	}
	for _, item := range items {
		data.Items = append(data.Items, htmlItem{
			Identifier: item.Identifier,
			Title:      item.Title,
			AgeDays:    item.AgeDays,
			Percentile: item.Percentile,
			Suffix:     util.OrdinalSuffix(item.Percentile),
			AgeClass:   ageClass(item.Percentile),
			State:      formatState(item.StateName, item.StateType),
			StartDate:  formatStartDate(item.StartedAt),
			Assignee:   item.Assignee,
		})
	}
	if err := tmpl.Execute(os.Stdout, data); err != nil {
		fmt.Fprintf(os.Stderr, "error: render HTML: %v\n", err)
		os.Exit(1)
	}
}
