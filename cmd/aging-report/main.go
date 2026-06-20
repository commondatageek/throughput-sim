package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"forecasting/internal/sqlite"
)

type issue struct {
	Engineer    string `json:"engineer"`
	Team        string `json:"team"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	ProjectName string `json:"project_name"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	Status      string `json:"status"`
}

type reportItem struct {
	Identifier  string
	Title       string
	Assignee    string
	ProjectName string
	StartedAt   time.Time
	AgeDays     float64
	Percentile  int
}

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}

var durationTermRe = regexp.MustCompile(`^([0-9]*\.?[0-9]+)([a-zµ]+)`)

// parseFlexibleDuration parses a duration string, extending time.ParseDuration
// with a "d" (day) unit so expressions like "1d" or "1d12h" are accepted.
func parseFlexibleDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if s == "" {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	var total time.Duration
	rest := s
	for rest != "" {
		m := durationTermRe.FindStringSubmatch(rest)
		if m == nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		var unitDur time.Duration
		if m[2] == "d" {
			unitDur = 24 * time.Hour
		} else {
			d, err := time.ParseDuration("1" + m[2])
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q: unknown unit %q", s, m[2])
			}
			unitDur = d
		}
		total += time.Duration(n * float64(unitDur))
		rest = rest[len(m[0]):]
	}
	return total, nil
}

func ordinalSuffix(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return "th"
	case n%10 == 1:
		return "st"
	case n%10 == 2:
		return "nd"
	case n%10 == 3:
		return "rd"
	default:
		return "th"
	}
}

// computePercentile returns what percentile v falls at in a sorted slice.
func computePercentile(sorted []float64, v float64) int {
	if len(sorted) == 0 {
		return 0
	}
	rank := sort.Search(len(sorted), func(i int) bool { return sorted[i] > v })
	return int(math.Round(float64(rank) / float64(len(sorted)) * 100))
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

// loadFromDB reads cycle times and in-progress items from the SQLite store.
func loadFromDB(dbPath string, sampleStart, sampleEnd time.Time, minCycleTime time.Duration, today time.Time) (cycleTimes []float64, inProgress []reportItem, err error) {
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	completed, err := store.CompletedBetween(context.Background(), "linear", sampleStart, sampleEnd, nil)
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

	active, err := store.InProgress(context.Background(), "linear")
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
			StartedAt:   it.StartedAt,
			AgeDays:     ageDays,
		})
	}

	return cycleTimes, inProgress, nil
}

func main() {
	dbFile := flag.String("db", "items.db", "path to SQLite database (default source)")
	issuesFile := flag.String("issues", "", "path to NDJSON issues file (overrides -db)")
	sampleStartStr := flag.String("sample-start", "", "Start of completed-issue window (YYYY-MM-DD, default: today minus 3 months)")
	sampleEndStr := flag.String("sample-end", "", "End of completed-issue window (YYYY-MM-DD, default: today)")
	format := flag.String("format", "text", "Output format: text, json, html")
	minCycleTimeStr := flag.String("min-cycle-time", "", "Exclude completed issues with cycle time below this duration from the percentile distribution (e.g. 5m, 1h, 1d)")
	flag.Parse()

	var err error
	var minCycleTime time.Duration
	if *minCycleTimeStr != "" {
		d, err := parseFlexibleDuration(*minCycleTimeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -min-cycle-time %q: %v\n", *minCycleTimeStr, err)
			os.Exit(1)
		}
		minCycleTime = d
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)

	sampleEnd := today
	if *sampleEndStr != "" {
		t, err := parseDate(*sampleEndStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid -sample-end %q: %v\n", *sampleEndStr, err)
			os.Exit(1)
		}
		sampleEnd = t
	}

	sampleStart := today.AddDate(0, -3, 0)
	if *sampleStartStr != "" {
		t, err := parseDate(*sampleStartStr)
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

	var cycleTimes []float64
	var inProgress []reportItem

	if *issuesFile != "" {
		// Legacy NDJSON path — used when -issues is explicitly provided.
		f, err := os.Open(*issuesFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: open %s: %v\n", *issuesFile, err)
			os.Exit(1)
		}
		defer f.Close()

		dec := json.NewDecoder(f)
		for dec.More() {
			var iss issue
			if err := dec.Decode(&iss); err != nil {
				fmt.Fprintf(os.Stderr, "error: decode issue: %v\n", err)
				os.Exit(1)
			}

			switch iss.Status {
			case "completed":
				if iss.StartedAt == "" || iss.CompletedAt == "" {
					continue
				}
				startedAt, err1 := time.Parse(time.RFC3339, iss.StartedAt)
				completedAt, err2 := time.Parse(time.RFC3339, iss.CompletedAt)
				if err1 != nil || err2 != nil {
					continue
				}
				if completedAt.Before(sampleStart) || completedAt.After(sampleEnd) {
					continue
				}
				cycleTime := completedAt.Sub(startedAt)
				if cycleTime < minCycleTime {
					continue
				}
				days := cycleTime.Hours() / 24
				if days >= 0 {
					cycleTimes = append(cycleTimes, days)
				}

			case "in_progress":
				if iss.StartedAt == "" {
					continue
				}
				startedAt, err := time.Parse(time.RFC3339, iss.StartedAt)
				if err != nil {
					continue
				}
				ageDays := today.Sub(startedAt).Hours() / 24
				if ageDays < 0 {
					ageDays = 0
				}
				inProgress = append(inProgress, reportItem{
					Identifier:  iss.Identifier,
					Title:       iss.Title,
					Assignee:    iss.Engineer,
					ProjectName: iss.ProjectName,
					StartedAt:   startedAt,
					AgeDays:     ageDays,
				})
			}
		}
	} else {
		// SQLite path — default.
		cycleTimes, inProgress, err = loadFromDB(*dbFile, sampleStart, sampleEnd, minCycleTime, today)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	sort.Float64s(cycleTimes)

	for i := range inProgress {
		inProgress[i].Percentile = computePercentile(cycleTimes, inProgress[i].AgeDays)
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
	fmt.Fprintln(w, "IDENTIFIER\tTITLE\tDAYS\tPERCENTILE\tSTART DATE\tASSIGNEE")
	for _, item := range items {
		pct := item.Percentile
		fmt.Fprintf(w, "%s\t%s\t%.1f\t%d%s\t%s\t%s\n",
			item.Identifier,
			item.Title,
			item.AgeDays,
			pct, ordinalSuffix(pct),
			formatStartDate(item.StartedAt),
			item.Assignee,
		)
	}
	w.Flush()
}

type jsonItem struct {
	Identifier  string  `json:"identifier"`
	Title       string  `json:"title"`
	Assignee    string  `json:"assignee"`
	ProjectName string  `json:"project_name,omitempty"`
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
			Suffix:     ordinalSuffix(item.Percentile),
			AgeClass:   ageClass(item.Percentile),
			StartDate:  formatStartDate(item.StartedAt),
			Assignee:   item.Assignee,
		})
	}
	if err := tmpl.Execute(os.Stdout, data); err != nil {
		fmt.Fprintf(os.Stderr, "error: render HTML: %v\n", err)
		os.Exit(1)
	}
}
