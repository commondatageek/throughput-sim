package counts

import (
	"testing"
	"time"

	"forecasting/internal/sqlite"
)

func mustTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t.UTC()
}

func TestComputeFoldsAndFilters(t *testing.T) {
	counts := []sqlite.ProjectMilestoneCount{
		{TeamKey: "ENG", TeamName: "Engineering", ProjectName: "Alpha", MilestoneName: "M1", Count: 3},
		{TeamKey: "ENG", TeamName: "Engineering", ProjectName: "Alpha", MilestoneName: "M2", Count: 2},
		{TeamKey: "ENG", TeamName: "Engineering", ProjectName: "Beta", MilestoneName: "", Count: 5},
	}
	activity := []sqlite.ProjectActivity{
		{TeamKey: "ENG", ProjectName: "Alpha", LastUpdated: mustTime("2024-03-01")},
		{TeamKey: "ENG", ProjectName: "Beta", LastUpdated: mustTime("2024-01-01")}, // old
	}

	// since=2024-02-01: Beta (last updated 2024-01-01) should be filtered out.
	projects, total := Compute(counts, activity, mustTime("2024-02-01"))

	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1 (Beta should be filtered)", len(projects))
	}
	if projects[0].Name != "Alpha" {
		t.Errorf("expected Alpha, got %s", projects[0].Name)
	}
	if projects[0].Total != 5 {
		t.Errorf("Alpha total: got %d, want 5", projects[0].Total)
	}
	if total != 5 {
		t.Errorf("grand total: got %d, want 5", total)
	}
	if len(projects[0].Milestones) != 2 {
		t.Errorf("Alpha milestones: got %d, want 2", len(projects[0].Milestones))
	}
}

func TestComputeNoProjectLabel(t *testing.T) {
	counts := []sqlite.ProjectMilestoneCount{
		{TeamKey: "ENG", TeamName: "Engineering", ProjectName: "", MilestoneName: "", Count: 4},
	}
	activity := []sqlite.ProjectActivity{
		{TeamKey: "ENG", ProjectName: "", LastUpdated: mustTime("2024-03-01")},
	}

	projects, _ := Compute(counts, activity, mustTime("2024-01-01"))
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	if projects[0].Name != NoProjectLabel {
		t.Errorf("expected %q, got %q", NoProjectLabel, projects[0].Name)
	}
	if projects[0].Milestones[0].Name != NoMilestoneLabel {
		t.Errorf("expected %q, got %q", NoMilestoneLabel, projects[0].Milestones[0].Name)
	}
}

func TestComputeSortByLastUpdated(t *testing.T) {
	counts := []sqlite.ProjectMilestoneCount{
		{TeamKey: "ENG", ProjectName: "Older", Count: 1},
		{TeamKey: "ENG", ProjectName: "Newer", Count: 1},
	}
	activity := []sqlite.ProjectActivity{
		{TeamKey: "ENG", ProjectName: "Older", LastUpdated: mustTime("2024-01-01")},
		{TeamKey: "ENG", ProjectName: "Newer", LastUpdated: mustTime("2024-03-01")},
	}

	projects, _ := Compute(counts, activity, time.Time{})
	if projects[0].Name != "Newer" {
		t.Errorf("first project should be Newer (most recent), got %s", projects[0].Name)
	}
}

func TestMilestonesSortedNoMilestoneLast(t *testing.T) {
	counts := []sqlite.ProjectMilestoneCount{
		{TeamKey: "ENG", ProjectName: "P", MilestoneName: "", Count: 1},
		{TeamKey: "ENG", ProjectName: "P", MilestoneName: "Zed", Count: 2},
		{TeamKey: "ENG", ProjectName: "P", MilestoneName: "Alpha", Count: 3},
	}
	activity := []sqlite.ProjectActivity{
		{TeamKey: "ENG", ProjectName: "P", LastUpdated: mustTime("2024-03-01")},
	}

	projects, _ := Compute(counts, activity, time.Time{})
	ms := projects[0].Milestones

	if ms[0].Name != "Alpha" {
		t.Errorf("first milestone should be Alpha, got %s", ms[0].Name)
	}
	if ms[1].Name != "Zed" {
		t.Errorf("second milestone should be Zed, got %s", ms[1].Name)
	}
	if ms[2].Name != NoMilestoneLabel {
		t.Errorf("last milestone should be %s, got %s", NoMilestoneLabel, ms[2].Name)
	}
}
