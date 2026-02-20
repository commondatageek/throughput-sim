package main

import (
	"bytes"
	"encoding/json"
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

// --- Cache structures (what we store on disk) ---

type EngineerCache struct {
	Counts        []int    `json:"counts"`
	ExcludedDates []string `json:"excludedDates"`
}

type SimCache struct {
	StartDate           string                    `json:"startDate"`
	EndDate             string                    `json:"endDate"`
	Engineers           map[string]*EngineerCache `json:"engineers"`
	GlobalExcludedDates []string                  `json:"globalExcludedDates"`
}

// --- In-memory sample pool ---

type SamplePool struct {
	Samples []int // valid (non-excluded) daily completion counts
}

func (p *SamplePool) Draw(rng *rand.Rand) int {
	return p.Samples[rng.Intn(len(p.Samples))]
}

// --- Build cache from raw issues ---

func buildCache(issues []RawIssue, startDate, endDate time.Time, globalExcluded []string) *SimCache {
	cache := &SimCache{
		StartDate:           startDate.Format("2006-01-02"),
		EndDate:             endDate.Format("2006-01-02"),
		Engineers:           make(map[string]*EngineerCache),
		GlobalExcludedDates: globalExcluded,
	}

	totalDays := int(endDate.Sub(startDate).Hours()/24) + 1

	// Initialize all engineers with zero counts
	for _, issue := range issues {
		if _, ok := cache.Engineers[issue.Engineer]; !ok {
			cache.Engineers[issue.Engineer] = &EngineerCache{
				Counts: make([]int, totalDays),
			}
		}
	}

	// Fill in counts
	for _, issue := range issues {
		t, err := time.Parse(time.RFC3339, issue.CompletedAt)
		if err != nil {
			continue
		}
		t = t.UTC().Truncate(24 * time.Hour)
		idx := int(t.Sub(startDate).Hours() / 24)
		if idx >= 0 && idx < totalDays {
			cache.Engineers[issue.Engineer].Counts[idx]++
		}
	}

	return cache
}

// --- Load cache into a pooled SamplePool ---

func loadPooledSamples(cache *SimCache, includeEngineers []string) (*SamplePool, error) {
	includeSet := make(map[string]bool, len(includeEngineers))
	for _, name := range includeEngineers {
		includeSet[name] = true
	}
	startDate, err := time.Parse("2006-01-02", cache.StartDate)
	if err != nil {
		return nil, err
	}

	// Build a set of globally excluded date indices
	excluded := make(map[int]bool)
	for _, ds := range cache.GlobalExcludedDates {
		t, err := time.Parse("2006-01-02", ds)
		if err != nil {
			continue
		}
		idx := int(t.Sub(startDate).Hours() / 24)
		excluded[idx] = true
	}

	pool := &SamplePool{}

	for name, eng := range cache.Engineers {
		if len(includeSet) > 0 && !includeSet[name] {
			continue
		}
		// Per-engineer excluded dates
		engExcluded := make(map[int]bool)
		for k, v := range excluded {
			engExcluded[k] = v
		}
		for _, ds := range eng.ExcludedDates {
			t, err := time.Parse("2006-01-02", ds)
			if err != nil {
				continue
			}
			idx := int(t.Sub(startDate).Hours() / 24)
			engExcluded[idx] = true
		}

		for i, count := range eng.Counts {
			if !engExcluded[i] {
				pool.Samples = append(pool.Samples, count)
			}
		}
	}

	return pool, nil
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

func loadPool(issuesFile string, includeEngineers []string, startDate, endDate time.Time) (*SamplePool, error) {
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

	cache := buildCache(issues, startDate, endDate, nil)
	return loadPooledSamples(cache, includeEngineers)
}

func defaultDateRange() (start, end string) {
	now := time.Now().UTC()
	return now.AddDate(0, -6, 0).Format("2006-01-02"), now.Format("2006-01-02")
}

func cmdItems(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("items", flag.ExitOnError)
	issuesFile := cmd.String("issues", "issues.json", "path to issues JSON file")
	engineers := cmd.Int("engineers", 3, "number of engineers")
	days := cmd.Int("days", 30, "number of days")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	var percentiles percentileList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 5,10,...,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	cmd.Parse(args)

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := parseDate(*sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*issuesFile, include, startDate, endDate)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	dist := SimulateItemsInDays(pool, *engineers, *days, *simulations, rng)
	fmt.Printf("%d engineers, %d days -> how many items?\n", *engineers, *days)
	if len(percentiles) > 0 {
		for _, p := range percentiles {
			fmt.Printf("  %dth percentile: %d items\n", p, Percentile(dist, float64(p)))
		}
	} else {
		for p := 5; p <= 95; p += 5 {
			fmt.Printf("  %dth percentile: %d items\n", p, Percentile(dist, float64(p)))
		}
	}
	return nil
}

func cmdDays(args []string) error {
	defaultSampleStart, defaultSampleEnd := defaultDateRange()
	cmd := flag.NewFlagSet("days", flag.ExitOnError)
	issuesFile := cmd.String("issues", "issues.json", "path to issues JSON file")
	engineers := cmd.Int("engineers", 3, "number of engineers")
	items := cmd.Int("items", 50, "number of items to complete")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sampleStart := cmd.String("sample-start", defaultSampleStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultSampleEnd, "sample data end date (YYYY-MM-DD)")
	var percentiles percentileList
	cmd.Var(&percentiles, "percentile", "comma-separated percentiles to output (default: 5,10,...,95)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	cmd.Parse(args)

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := parseDate(*sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*issuesFile, include, startDate, endDate)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	dist := SimulateDaysToComplete(pool, *engineers, *items, *simulations, rng)
	fmt.Printf("%d engineers, %d items -> how many days?\n", *engineers, *items)
	if len(percentiles) > 0 {
		for _, p := range percentiles {
			fmt.Printf("  %dth percentile: %d days\n", p, Percentile(dist, float64(p)))
		}
	} else {
		for p := 5; p <= 95; p += 5 {
			fmt.Printf("  %dth percentile: %d days\n", p, Percentile(dist, float64(p)))
		}
	}
	return nil
}

func cmdProbability(args []string) error {
	defaultStart, defaultEnd := defaultDateRange()
	cmd := flag.NewFlagSet("probability", flag.ExitOnError)
	issuesFile := cmd.String("issues", "issues.json", "path to issues JSON file")
	engineers := cmd.Int("engineers", 3, "number of engineers")
	days := cmd.Int("days", 30, "number of days")
	items := cmd.Int("items", 50, "number of items to complete")
	simulations := cmd.Int("simulations", 10_000, "number of Monte Carlo simulations to run")
	sampleStart := cmd.String("sample-start", defaultStart, "sample data start date (YYYY-MM-DD)")
	sampleEnd := cmd.String("sample-end", defaultEnd, "sample data end date (YYYY-MM-DD)")
	var include stringList
	cmd.Var(&include, "include", "comma-separated list of engineer names to include (default: all)")
	cmd.Parse(args)

	startDate, err := parseDate(*sampleStart)
	if err != nil {
		return fmt.Errorf("invalid -sample-start date: %w", err)
	}
	endDate, err := parseDate(*sampleEnd)
	if err != nil {
		return fmt.Errorf("invalid -sample-end date: %w", err)
	}

	pool, err := loadPool(*issuesFile, include, startDate, endDate)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	dist := SimulateItemsInDays(pool, *engineers, *days, *simulations, rng)

	count := 0
	for _, v := range dist {
		if v >= *items {
			count++
		}
	}
	probability := float64(count) / float64(*simulations) * 100.0

	fmt.Printf("%d engineers, %d days, %d items -> probability of completion?\n", *engineers, *days, *items)
	fmt.Printf("  %.1f%%\n", probability)
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
