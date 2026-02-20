package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// stringList is a flag.Value for a comma-separated list of strings.
type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(val string) error {
	*s = nil
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

// percentileList is a flag.Value for a comma-separated list of ints.
type percentileList []int

func (p *percentileList) String() string {
	parts := make([]string, len(*p))
	for i, v := range *p {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func (p *percentileList) Set(s string) error {
	*p = nil
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		v, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("invalid percentile %q: %w", part, err)
		}
		*p = append(*p, v)
	}
	return nil
}

// --- Data loading ---

type RawIssue struct {
	Engineer    string `json:"engineer"`
	Title       string `json:"title"`
	CompletedAt string `json:"completed_at"` // RFC 3339 date string
}

// --- Exclusions ---

type Exclusions struct {
	Global    []string            `json:"global"`
	Engineers map[string][]string `json:"engineers"`
}

func loadExclusions(path string) (Exclusions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Exclusions{}, nil
		}
		return Exclusions{}, fmt.Errorf("reading exclusions file: %w", err)
	}
	var exc Exclusions
	if err := json.Unmarshal(data, &exc); err != nil {
		return Exclusions{}, fmt.Errorf("parsing exclusions file: %w", err)
	}
	return exc, nil
}

// --- In-memory sample pool ---

type SamplePool struct {
	Samples []int // valid (non-excluded) daily completion counts
}

func (p *SamplePool) Draw(rng *rand.Rand) int {
	return p.Samples[rng.Intn(len(p.Samples))]
}

// --- Simulation ---

// SimulateItemsInDays runs N simulations and returns the distribution of
// total items completed in `days` days by `engineers` engineers.
func SimulateItemsInDays(pool *SamplePool, numEngineers, days, numSimulations int, rng *rand.Rand) []int {
	results := make([]int, numSimulations)
	for i := range results {
		total := 0
		for e := 0; e < numEngineers; e++ {
			for d := 0; d < days; d++ {
				total += pool.Draw(rng)
			}
		}
		results[i] = total
	}
	sort.Ints(results)
	return results
}

// SimulateDaysToComplete runs N simulations and returns the distribution of
// days needed for `engineers` engineers to complete `items` items.
func SimulateDaysToComplete(pool *SamplePool, numEngineers, items, numSimulations int, rng *rand.Rand) []int {
	results := make([]int, numSimulations)
	for i := range results {
		completed := 0
		days := 0
		for completed < items {
			days++
			for e := 0; e < numEngineers; e++ {
				completed += pool.Draw(rng)
			}
		}
		results[i] = days
	}
	sort.Ints(results)
	return results
}

// Percentile returns the value at the given percentile (0-100) from a sorted slice.
func Percentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p/100.0*float64(len(sorted)-1) + 0.5)
	return sorted[idx]
}

// --- Main ---

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: sim <command> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  items        How many items can N engineers complete in D days?\n")
	fmt.Fprintf(os.Stderr, "  days         How many days for N engineers to complete I items?\n")
	fmt.Fprintf(os.Stderr, "  probability  What is the probability of completing I items in D days?\n\n")
	fmt.Fprintf(os.Stderr, "Run 'sim <command> -help' for command-specific flags.\n")
}

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}

func loadPool(issuesFile, exclusionsFile string, includeEngineers []string, startDate, endDate time.Time, wholeTeam bool) (*SamplePool, error) {
	data, err := os.ReadFile(issuesFile)
	if err != nil {
		return nil, fmt.Errorf("reading issues file: %w", err)
	}
	var issues []RawIssue
	decoder := json.NewDecoder(bytes.NewReader(data))
	for decoder.More() {
		var issue RawIssue
		if err := decoder.Decode(&issue); err != nil {
			return nil, fmt.Errorf("decoding issue: %w", err)
		}
		issues = append(issues, issue)
	}

	exc, err := loadExclusions(exclusionsFile)
	if err != nil {
		return nil, err
	}

	totalDays := int(endDate.Sub(startDate).Hours()/24) + 1

	// Build global excluded date index set
	globalExcluded := make(map[int]bool)
	for _, ds := range exc.Global {
		t, err := time.ParseInLocation("2006-01-02", ds, time.UTC)
		if err != nil {
			continue
		}
		idx := int(t.Sub(startDate).Hours() / 24)
		globalExcluded[idx] = true
	}

	// Collect unique engineers (respecting include filter)
	includeSet := make(map[string]bool, len(includeEngineers))
	for _, name := range includeEngineers {
		includeSet[name] = true
	}
	engineerSeen := make(map[string]bool)
	for _, issue := range issues {
		engineerSeen[issue.Engineer] = true
	}

	// Build per-day counts per engineer
	type engData struct {
		counts []int
	}
	engineers := make(map[string]*engData)
	for name := range engineerSeen {
		if len(includeSet) > 0 && !includeSet[name] {
			continue
		}
		engineers[name] = &engData{counts: make([]int, totalDays)}
	}

	for _, issue := range issues {
		eng, ok := engineers[issue.Engineer]
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339, issue.CompletedAt)
		if err != nil {
			continue
		}
		t = t.UTC().Truncate(24 * time.Hour)
		idx := int(t.Sub(startDate).Hours() / 24)
		if idx >= 0 && idx < totalDays {
			eng.counts[idx]++
		}
	}

	pool := &SamplePool{}

	if wholeTeam {
		// Sum all engineers' completions per day into a single team-level sample.
		// Only global exclusions apply — per-engineer exclusions are irrelevant when
		// treating the team as a unit.
		teamCounts := make([]int, totalDays)
		for _, eng := range engineers {
			for i, count := range eng.counts {
				teamCounts[i] += count
			}
		}
		for i, count := range teamCounts {
			if !globalExcluded[i] {
				pool.Samples = append(pool.Samples, count)
			}
		}
	} else {
		for name, eng := range engineers {
			// Per-engineer excluded set = global + engineer-specific
			excluded := make(map[int]bool, len(globalExcluded))
			for k := range globalExcluded {
				excluded[k] = true
			}
			for _, ds := range exc.Engineers[name] {
				t, err := time.ParseInLocation("2006-01-02", ds, time.UTC)
				if err != nil {
					continue
				}
				idx := int(t.Sub(startDate).Hours() / 24)
				excluded[idx] = true
			}

			for i, count := range eng.counts {
				if !excluded[i] {
					pool.Samples = append(pool.Samples, count)
				}
			}
		}
	}

	return pool, nil
}

// isFlagSet reports whether a flag was explicitly provided on the command line.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func defaultDateRange() (start, end string) {
	now := time.Now().UTC()
	return now.AddDate(0, -6, 0).Format("2006-01-02"), now.Format("2006-01-02")
}

func cmdItems(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("items", flag.ExitOnError)
	issuesFile := cmd.String("issues", "issues.json", "path to issues JSON file")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of engineers")
	days := cmd.Int("days", 30, "number of days")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	var percentiles percentileList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 5,25,50,75,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	cmd.Parse(args)

	if *wholeTeam && isFlagSet(cmd, "engineers") {
		return fmt.Errorf("-whole-team and -engineers are mutually exclusive")
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := parseDate(*sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*issuesFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	numEngineers := *engineers
	if *wholeTeam {
		numEngineers = 1
	}

	dist := SimulateItemsInDays(pool, numEngineers, *days, *simulations, rng)
	if *wholeTeam {
		fmt.Printf("whole-team throughput, %d days -> how many items?\n", *days)
	} else {
		fmt.Printf("%d engineers, %d days -> how many items?\n", numEngineers, *days)
	}
	if len(percentiles) == 0 {
		percentiles = percentileList{5, 25, 50, 75, 95}
	}
	for _, p := range percentiles {
		fmt.Printf("  %dth percentile: %d items\n", p, Percentile(dist, float64(p)))
	}
	return nil
}

func cmdDays(args []string) error {
	defaultSampleStart, defaultSampleEnd := defaultDateRange()
	cmd := flag.NewFlagSet("days", flag.ExitOnError)
	issuesFile := cmd.String("issues", "issues.json", "path to issues JSON file")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of engineers")
	items := cmd.Int("items", 50, "number of items to complete")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sampleStart := cmd.String("sample-start", defaultSampleStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultSampleEnd, "sample data end date (YYYY-MM-DD)")
	var percentiles percentileList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 50,75,85,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	cmd.Parse(args)

	if *wholeTeam && isFlagSet(cmd, "engineers") {
		return fmt.Errorf("-whole-team and -engineers are mutually exclusive")
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := parseDate(*sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*issuesFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	numEngineers := *engineers
	if *wholeTeam {
		numEngineers = 1
	}

	dist := SimulateDaysToComplete(pool, numEngineers, *items, *simulations, rng)
	if *wholeTeam {
		fmt.Printf("whole-team throughput, %d items -> how many days?\n", *items)
	} else {
		fmt.Printf("%d engineers, %d items -> how many days?\n", numEngineers, *items)
	}
	if len(percentiles) == 0 {
		percentiles = percentileList{50, 75, 85, 95}
	}
	for _, p := range percentiles {
		fmt.Printf("  %dth percentile: %d days\n", p, Percentile(dist, float64(p)))
	}
	return nil
}

func cmdProbability(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("probability", flag.ExitOnError)
	issuesFile := cmd.String("issues", "issues.json", "path to issues JSON file")
	exclusionsFile := cmd.String("exclusions", "exclusions.json", "path to exclusions JSON file")
	engineers := cmd.Int("engineers", 3, "number of engineers")
	days := cmd.Int("days", 30, "number of days")
	items := cmd.Int("items", -1, "number of items to complete (omit to show full distribution)")
	wholeTeam := cmd.Bool("whole-team", false, "use whole-team daily throughput from historical data (ignores -engineers)")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	cmd.Parse(args)

	if *wholeTeam && isFlagSet(cmd, "engineers") {
		return fmt.Errorf("-whole-team and -engineers are mutually exclusive")
	}

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := parseDate(*sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*issuesFile, *exclusionsFile, include, startDate, endDate, *wholeTeam)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	numEngineers := *engineers
	if *wholeTeam {
		numEngineers = 1
	}

	dist := SimulateItemsInDays(pool, numEngineers, *days, *simulations, rng)

	probFor := func(n int) float64 {
		count := 0
		for _, v := range dist {
			if v >= n {
				count++
			}
		}
		return float64(count) / float64(*simulations) * 100.0
	}

	if *wholeTeam {
		if *items >= 0 {
			fmt.Printf("whole-team throughput, %d days, %d items -> probability of completion?\n", *days, *items)
			fmt.Printf("  %.1f%%\n", probFor(*items))
		} else {
			fmt.Printf("whole-team throughput, %d days -> probability of completing N items\n", *days)
			for n := 1; ; n++ {
				p := probFor(n)
				fmt.Printf("  %d items: %.1f%%\n", n, p)
				if p == 0 {
					break
				}
			}
		}
	} else {
		if *items >= 0 {
			fmt.Printf("%d engineers, %d days, %d items -> probability of completion?\n", numEngineers, *days, *items)
			fmt.Printf("  %.1f%%\n", probFor(*items))
		} else {
			fmt.Printf("%d engineers, %d days -> probability of completing N items\n", numEngineers, *days)
			for n := 1; ; n++ {
				p := probFor(n)
				fmt.Printf("  %d items: %.1f%%\n", n, p)
				if p == 0 {
					break
				}
			}
		}
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "items":
		err = cmdItems(os.Args[2:])
	case "days":
		err = cmdDays(os.Args[2:])
	case "probability":
		err = cmdProbability(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
