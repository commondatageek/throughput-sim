package simulate

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"forecasting/internal/util"
)

// WholeTeamKey is the SamplePool.PerEngineer key used in whole-team mode,
// where all engineers' daily counts are summed into a single series.
const WholeTeamKey = "__whole_team__"

// Completion is a normalized record of a completed unit of work: the engineer
// who completed it and when. The cmd layer converts source-specific records
// (e.g. linear.Issue) to Completion before building a pool.
type Completion struct {
	Engineer    string
	CompletedAt time.Time
}

// FilterInvalid removes Completion records with an empty Engineer or zero
// CompletedAt and returns the count of dropped records. CompletedBetween
// already filters these out in practice; this is belt-and-suspenders in case
// a future loader lets a bad record through.
func FilterInvalid(records []Completion) ([]Completion, int) {
	out := make([]Completion, 0, len(records))
	skipped := 0
	for _, r := range records {
		if r.Engineer == "" || r.CompletedAt.IsZero() {
			skipped++
			continue
		}
		out = append(out, r)
	}
	return out, skipped
}

// Exclusions lists calendar dates to exclude from the sample pool.
type Exclusions struct {
	Global    []string            `json:"global"`
	Engineers map[string][]string `json:"engineers"`
}

// ParseExclusions parses exclusions JSON data, as read from an exclusions
// file by the caller.
func ParseExclusions(data []byte) (Exclusions, error) {
	var exc Exclusions
	if err := json.Unmarshal(data, &exc); err != nil {
		return Exclusions{}, fmt.Errorf("parsing exclusions file: %w", err)
	}
	return exc, nil
}

// SamplePool holds per-engineer slices of daily completion counts, plus the
// precomputed flattened view (Combined) that anonymous-mode simulations draw
// from. Construct via NewSamplePool (or BuildPool) so Combined is always
// populated; a zero-value SamplePool leaves it nil.
type SamplePool struct {
	PerEngineer map[string][]int
	Combined    []int
}

// NewSamplePool builds a SamplePool from per-engineer sample slices,
// precomputing Combined so simulation code never has to derive it.
func NewSamplePool(perEngineer map[string][]int) *SamplePool {
	return &SamplePool{
		PerEngineer: perEngineer,
		Combined:    combineSamples(perEngineer),
	}
}

// DrawFromEngineer randomly samples one daily completion count for engineer.
func (p *SamplePool) DrawFromEngineer(engineer string, rng *rand.Rand) int {
	samples := p.PerEngineer[engineer]
	return samples[rng.Intn(len(samples))]
}

// combineSamples concatenates all engineers' samples into a flat slice,
// ordered by engineer name so the result (and thus anything sampled from it
// under a pinned seed) is deterministic across runs.
func combineSamples(perEngineer map[string][]int) []int {
	names := make([]string, 0, len(perEngineer))
	for name := range perEngineer {
		names = append(names, name)
	}
	sort.Strings(names)

	var combined []int
	for _, name := range names {
		combined = append(combined, perEngineer[name]...)
	}
	return combined
}

// DaysBetween returns the number of per-day sample slots in [start, end).
// end is normally a calendar date at midnight, in which case that day is
// fully excluded. If end carries a time-of-day (e.g. it's "now"), the day
// it falls on is partially in range, so it gets one inclusive slot.
func DaysBetween(start, end time.Time) int {
	endDay := util.LocalDay(end)
	days := util.DayIndex(endDay, start)
	if !end.Equal(endDay) {
		days++
	}
	return days
}

// BuildPool bins completions into per-engineer daily completion counts over the
// half-open window [startDate, endDate), applies exclusions, and returns the
// resulting SamplePool. It is pure (no file/DB/clock access).
//
// The pool deliberately preserves zero-completion days: each engineer's slice
// has one slot per non-excluded day in the window, so a day with no completions
// contributes a 0 sample. Dropping those would bias every forecast upward.
//
// The engineer set is derived solely from records: an engineer appears in the
// pool only if they have at least one completion inside the window. Completions
// outside the window are ignored entirely (neither counted nor do they create
// an engineer). In whole-team mode all engineers are summed into a single
// WholeTeamKey series.
func BuildPool(records []Completion, exc Exclusions, startDate, endDate time.Time, wholeTeam bool) *SamplePool {
	totalDays := DaysBetween(startDate, endDate)

	// Build the global excluded day-index set.
	globalExcluded := make(map[int]bool)
	for _, ds := range exc.Global {
		t, err := util.ParseDate(ds)
		if err != nil {
			continue
		}
		idx := util.DayIndex(t, startDate)
		globalExcluded[idx] = true
	}

	type engData struct {
		counts []int
	}
	engineers := make(map[string]*engData)
	for _, r := range records {
		t := util.LocalDay(r.CompletedAt)
		idx := util.DayIndex(t, startDate)
		if idx < 0 || idx >= totalDays {
			continue
		}
		eng, ok := engineers[r.Engineer]
		if !ok {
			eng = &engData{counts: make([]int, totalDays)}
			engineers[r.Engineer] = eng
		}
		eng.counts[idx]++
	}

	perEngineer := make(map[string][]int)

	if wholeTeam {
		teamCounts := make([]int, totalDays)
		for _, eng := range engineers {
			for i, count := range eng.counts {
				teamCounts[i] += count
			}
		}
		var teamSamples []int
		for i, count := range teamCounts {
			if !globalExcluded[i] {
				teamSamples = append(teamSamples, count)
			}
		}
		perEngineer[WholeTeamKey] = teamSamples
	} else {
		for name, eng := range engineers {
			excluded := make(map[int]bool, len(globalExcluded))
			for k := range globalExcluded {
				excluded[k] = true
			}
			for _, ds := range exc.Engineers[name] {
				t, err := util.ParseDate(ds)
				if err != nil {
					continue
				}
				idx := util.DayIndex(t, startDate)
				excluded[idx] = true
			}
			var engineerSamples []int
			for i, count := range eng.counts {
				if !excluded[i] {
					engineerSamples = append(engineerSamples, count)
				}
			}
			perEngineer[name] = engineerSamples
		}
	}

	return NewSamplePool(perEngineer)
}
