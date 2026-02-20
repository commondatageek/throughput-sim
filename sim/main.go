package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"time"
)

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

func loadPooledSamples(cache *SimCache) (*SamplePool, error) {
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

	for _, eng := range cache.Engineers {
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

// --- Main (example usage) ---

func main() {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Load raw issues
	data, err := os.ReadFile("issues.json")
	if err != nil {
		panic(err)
	}
	var issues []RawIssue
	decoder := json.NewDecoder(bytes.NewReader(data))
	for decoder.More() {
		var issue RawIssue
		if err := decoder.Decode(&issue); err != nil {
			panic(err)
		}
		issues = append(issues, issue)
	}

	// Build and save cache
	startDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	endDate := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	globalExcluded := []string{"2024-12-23", "2024-12-24", "2024-12-25", "2024-12-26"}

	cache := buildCache(issues, startDate, endDate, globalExcluded)

	cacheData, _ := json.MarshalIndent(cache, "", "  ")
	os.WriteFile("cache.json", cacheData, 0644)

	// Load pool
	pool, err := loadPooledSamples(cache)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Pool size: %d samples\n", len(pool.Samples))

	const N = 10_000
	engineers := 3

	// Question 1: How many items in 30 days?
	days := 30
	itemDist := SimulateItemsInDays(pool, engineers, days, N, rng)
	fmt.Printf("\n%d engineers, %d days:\n", engineers, days)
	fmt.Printf("  50th percentile: %d items\n", Percentile(itemDist, 50))
	fmt.Printf("  85th percentile: %d items\n", Percentile(itemDist, 85))
	fmt.Printf("  95th percentile: %d items\n", Percentile(itemDist, 95))

	// Question 2: How many days to complete 50 items?
	items := 50
	dayDist := SimulateDaysToComplete(pool, engineers, items, N, rng)
	fmt.Printf("\n%d engineers, %d items:\n", engineers, items)
	fmt.Printf("  50th percentile: %d days\n", Percentile(dayDist, 50))
	fmt.Printf("  85th percentile: %d days\n", Percentile(dayDist, 85))
	fmt.Printf("  95th percentile: %d days\n", Percentile(dayDist, 95))
}
