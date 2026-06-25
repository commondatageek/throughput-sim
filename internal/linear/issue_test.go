package linear

import (
	"testing"
	"time"
)

func TestToIssueNoAssigneeYieldsEmpty(t *testing.T) {
	n := issueNode{Identifier: "ENG-1", Assignee: nil}
	got := toIssue(n)
	if got.Assignee != "" {
		t.Fatalf("toIssue with nil Assignee = %q, want empty", got.Assignee)
	}
}

func TestToIssueKeepsIssueWithoutStartedAt(t *testing.T) {
	n := issueNode{
		Identifier: "ENG-1",
		Assignee:   &assignee{Name: "alice"},
		// CompletedAt and StartedAt both zero (e.g. a backlog issue).
	}
	got := toIssue(n)
	if got.Identifier != "ENG-1" {
		t.Fatalf("toIssue dropped a backlog issue: %+v", got)
	}
}

func TestToIssueFullyPopulated(t *testing.T) {
	createdAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	startedAt := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	completedAt := time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
	canceledAt := time.Date(2024, 1, 9, 0, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
	archivedAt := time.Date(2024, 1, 11, 0, 0, 0, 0, time.UTC)
	autoArchivedAt := time.Date(2024, 1, 12, 0, 0, 0, 0, time.UTC)
	addedToProjectAt := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)

	n := issueNode{
		Identifier:       "ENG-123",
		Title:            "Fix bug",
		CreatedAt:        createdAt,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		CanceledAt:       canceledAt,
		UpdatedAt:        updatedAt,
		ArchivedAt:       archivedAt,
		AutoArchivedAt:   autoArchivedAt,
		AddedToProjectAt: addedToProjectAt,
		Assignee:         &assignee{Name: "alice"},
		Team:             &teamRef{Key: "ENG", Name: "Engineering"},
		Project:          &projectRef{ID: "proj-1", Name: "Q3"},
		ProjectMilestone: &milestoneRef{ID: "ms-1", Name: "Milestone 1"},
		State:            &stateRef{Type: "completed", Name: "Done"},
	}

	got := toIssue(n)

	want := Issue{
		Identifier:           "ENG-123",
		Title:                "Fix bug",
		Assignee:             "alice",
		TeamKey:              "ENG",
		TeamName:             "Engineering",
		ProjectID:            "proj-1",
		ProjectName:          "Q3",
		ProjectMilestoneID:   "ms-1",
		ProjectMilestoneName: "Milestone 1",
		StateType:            "completed",
		StateName:            "Done",
		CreatedAt:            createdAt,
		StartedAt:            startedAt,
		CompletedAt:          completedAt,
		CanceledAt:           canceledAt,
		ArchivedAt:           archivedAt,
		AutoArchivedAt:       autoArchivedAt,
		AddedToProjectAt:     addedToProjectAt,
		UpdatedAt:            updatedAt,
	}
	if got != want {
		t.Fatalf("toIssue() = %+v, want %+v", got, want)
	}
}

func TestToIssueNilRelationsYieldEmptyStrings(t *testing.T) {
	n := issueNode{
		Identifier:  "ENG-1",
		Assignee:    &assignee{Name: "alice"},
		CompletedAt: time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC),
		// Team, Project, ProjectMilestone, State all nil.
	}

	got := toIssue(n)
	if got.TeamKey != "" || got.TeamName != "" || got.ProjectID != "" || got.ProjectName != "" ||
		got.ProjectMilestoneID != "" || got.ProjectMilestoneName != "" ||
		got.StateType != "" || got.StateName != "" {
		t.Fatalf("toIssue() with nil relations = %+v, want all relation fields empty", got)
	}
}
