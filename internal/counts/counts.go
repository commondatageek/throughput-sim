package counts

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/commondatageek/delivery-forecast/internal/linear"
	"github.com/commondatageek/delivery-forecast/internal/sqlite"
)

const (
	NoProjectLabel   = "(No Project)"
	NoMilestoneLabel = "(No Milestone)"
)

// Options controls which issues are counted. Every field mirrors a
// `forecast count` flag (and thus a `-config` YAML key of the same name).
type Options struct {
	// Teams is the `-teams` flag: team keys to filter by; empty means all teams.
	Teams linear.TeamKeyList
	// Since is the `-updated-since` flag, resolved to a concrete date
	// (default: today minus 3 months). Projects whose most recently updated
	// issue predates it are dropped.
	Since time.Time
}

// Project holds the milestone breakdown for a single project, its total
// not-completed issue count, and the timestamp of its most recently updated issue.
type Project struct {
	Name        string
	TeamName    string
	Total       int
	LastUpdated time.Time
	Milestones  []Milestone
}

// MilestoneCount returns the number of milestones with a real name (excluding
// the synthetic NoMilestoneLabel bucket).
func (p Project) MilestoneCount() int {
	n := 0
	for _, m := range p.Milestones {
		if m.Name != NoMilestoneLabel {
			n++
		}
	}
	return n
}

// Milestone is a named bucket within a project.
type Milestone struct {
	Name  string
	Count int
}

// Compute folds not-completed issue counts and project activity into a sorted
// list of Projects. Projects whose most recently updated issue predates since
// are dropped. The second return value is the grand total across all returned
// projects.
func Compute(counts []sqlite.ProjectMilestoneCount, activity []sqlite.ProjectActivity, since time.Time) ([]Project, int) {
	type key struct{ team, project string }

	lastUpdated := make(map[key]time.Time, len(activity))
	for _, a := range activity {
		lastUpdated[key{team: a.TeamKey, project: a.ProjectName}] = a.LastUpdated
	}

	byProject := make(map[key]*Project)
	var order []key
	for _, c := range counts {
		k := key{team: c.TeamKey, project: c.ProjectName}
		p, ok := byProject[k]
		if !ok {
			name := c.ProjectName
			if name == "" {
				name = NoProjectLabel
			}
			p = &Project{Name: name, TeamName: c.TeamName, LastUpdated: lastUpdated[k]}
			byProject[k] = p
			order = append(order, k)
		}
		msName := c.MilestoneName
		if msName == "" {
			msName = NoMilestoneLabel
		}
		p.Milestones = append(p.Milestones, Milestone{Name: msName, Count: c.Count})
		p.Total += c.Count
	}

	var projects []Project
	var total int
	for _, k := range order {
		p := byProject[k]
		if p.LastUpdated.Before(since) {
			continue
		}
		sortMilestones(p.Milestones)
		projects = append(projects, *p)
		total += p.Total
	}
	sortProjects(projects)

	return projects, total
}

// sortProjects orders projects by most recently updated issue first, breaking
// ties alphabetically by name.
func sortProjects(projects []Project) {
	sort.Slice(projects, func(i, j int) bool {
		if !projects[i].LastUpdated.Equal(projects[j].LastUpdated) {
			return projects[i].LastUpdated.After(projects[j].LastUpdated)
		}
		return projects[i].Name < projects[j].Name
	})
}

// sortMilestones orders milestones alphabetically, with NoMilestoneLabel last.
func sortMilestones(ms []Milestone) {
	sort.Slice(ms, func(i, j int) bool {
		ni, nj := ms[i].Name, ms[j].Name
		if (ni == NoMilestoneLabel) != (nj == NoMilestoneLabel) {
			return nj == NoMilestoneLabel
		}
		return ni < nj
	})
}

// RenderSummary writes a one-line-per-project summary table to w.
func RenderSummary(w io.Writer, projects []Project, total int, showTeams bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if showTeams {
		fmt.Fprintln(tw, "PROJECT\tTEAM\tISSUES\tMILESTONES")
		for _, p := range projects {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", p.Name, p.TeamName, p.Total, p.MilestoneCount())
		}
		fmt.Fprintf(tw, "TOTAL\t\t%d\t\n", total)
	} else {
		fmt.Fprintln(tw, "PROJECT\tISSUES\tMILESTONES")
		for _, p := range projects {
			fmt.Fprintf(tw, "%s\t%d\t%d\n", p.Name, p.Total, p.MilestoneCount())
		}
		fmt.Fprintf(tw, "TOTAL\t%d\t\n", total)
	}
	return tw.Flush()
}

// RenderGrouped writes a per-project grouped view with per-milestone breakdowns to w.
func RenderGrouped(w io.Writer, projects []Project, total int, showTeams bool) error {
	for _, p := range projects {
		if showTeams && p.TeamName != "" {
			fmt.Fprintf(w, "%s [%s] (%d)\n", p.Name, p.TeamName, p.Total)
		} else {
			fmt.Fprintf(w, "%s (%d)\n", p.Name, p.Total)
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, m := range p.Milestones {
			fmt.Fprintf(tw, "  %s\t%d\n", m.Name, m.Count)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "TOTAL: %d not-completed issues\n", total)
	return nil
}
