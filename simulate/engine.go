package simulate

import (
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
)

// Sum returns the total of samples.
func Sum(samples []int) int {
	total := 0
	for _, v := range samples {
		total += v
	}
	return total
}

// RunSimulations runs numSimulations independent trials across numWorkers
// goroutines and returns the sorted distribution of their results. Each worker
// owns a disjoint range of the results slice and gets its own *rand.Rand seeded
// from seed plus its worker index (rand.Rand is not safe for concurrent use).
// progress is called with (done, total) after each trial; nil means no-op.
// Do not change the chunk math: seed reproducibility depends on (seed, numWorkers).
func RunSimulations(numSimulations, numWorkers int, seed int64, trial func(rng *rand.Rand) int, progress func(done, total int)) []int {
	if numWorkers < 1 {
		numWorkers = 1
	}
	if progress == nil {
		progress = func(_, _ int) {}
	}
	results := make([]int, numSimulations)
	var done atomic.Int64
	var wg sync.WaitGroup

	chunk := (numSimulations + numWorkers - 1) / numWorkers
	for w := 0; w < numWorkers; w++ {
		start := w * chunk
		if start >= numSimulations {
			break
		}
		end := min(start+chunk, numSimulations)
		wg.Add(1)
		go func(start, end, w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed + int64(w)))
			for i := start; i < end; i++ {
				results[i] = trial(rng)
				progress(int(done.Add(1)), numSimulations)
			}
		}(start, end, w)
	}
	wg.Wait()
	sort.Ints(results)
	return results
}

// SimulateItemsInDays returns the distribution of total items completed in
// `days` days by `numDailyDraws` equivalent engineers sampling from samples.
func SimulateItemsInDays(samples []int, numDailyDraws, days, numSimulations, numWorkers int, seed int64, progress func(done, total int)) []int {
	return RunSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		total := 0
		for e := 0; e < numDailyDraws; e++ {
			for d := 0; d < days; d++ {
				total += samples[rng.Intn(len(samples))]
			}
		}
		return total
	}, progress)
}

// SimulateItemsInDaysPerEngineer returns the distribution of total items
// completed in `days` days where each engineer samples their own history.
func SimulateItemsInDaysPerEngineer(pool *SamplePool, teamMembers []string, days, numSimulations, numWorkers int, seed int64, progress func(done, total int)) []int {
	return RunSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		total := 0
		for _, engineer := range teamMembers {
			for d := 0; d < days; d++ {
				total += pool.DrawFromEngineer(engineer, rng)
			}
		}
		return total
	}, progress)
}

// SimulateDaysToComplete returns the distribution of days needed for
// `numEngineers` equivalent engineers to complete `items` items sampling from samples.
func SimulateDaysToComplete(samples []int, numEngineers, items, numSimulations, numWorkers int, seed int64, progress func(done, total int)) []int {
	return RunSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		completed := 0
		days := 0
		for completed < items {
			days++
			for e := 0; e < numEngineers; e++ {
				completed += samples[rng.Intn(len(samples))]
			}
		}
		return days
	}, progress)
}

// SimulateDaysToCompletePerEngineer returns the distribution of days needed
// where each engineer samples their own history.
func SimulateDaysToCompletePerEngineer(pool *SamplePool, teamMembers []string, items, numSimulations, numWorkers int, seed int64, progress func(done, total int)) []int {
	return RunSimulations(numSimulations, numWorkers, seed, func(rng *rand.Rand) int {
		completed := 0
		days := 0
		for completed < items {
			days++
			for _, engineer := range teamMembers {
				completed += pool.DrawFromEngineer(engineer, rng)
			}
		}
		return days
	}, progress)
}

// ProbabilityAtLeast returns the percentage (0-100) of results in dist that
// met or exceeded n. Returns 0 for an empty distribution.
func ProbabilityAtLeast(dist []int, n int) float64 {
	if len(dist) == 0 {
		return 0
	}
	count := 0
	for _, v := range dist {
		if v >= n {
			count++
		}
	}
	return float64(count) / float64(len(dist)) * 100.0
}
