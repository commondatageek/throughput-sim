package linear

import (
	"testing"
	"time"
)

func TestToIssueSkipsNoAssignee(t *testing.T) {
	n := issueNode{Identifier: "ENG-1", Assignee: nil}
	if _, ok := toIssue(n); ok {
		t.Fatalf("toIssue with nil Assignee should be skipped")
	}
}

func TestToIssueSkipsInProgressWithoutStartedAt(t *testing.T) {
	n := issueNode{
		Identifier: "ENG-1",
		Assignee:   &assignee{Name: "alice"},
		// CompletedAt and StartedAt both zero.
	}
	if _, ok := toIssue(n); ok {
		t.Fatalf("toIssue for an in-progress issue with no startedAt should be skipped")
	}
}

func TestToIssueFullyPopulated(t *testing.T) {
	createdAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	startedAt := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	completedAt := time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
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
		UpdatedAt:        updatedAt,
		ArchivedAt:       archivedAt,
		AutoArchivedAt:   autoArchivedAt,
		AddedToProjectAt: addedToProjectAt,
		Assignee:         &assignee{Name: "alice"},
		Team:             &teamRef{Name: "ENG"},
		Project:          &projectRef{ID: "proj-1", Name: "Q3"},
		ProjectMilestone: &milestoneRef{ID: "ms-1", Name: "Milestone 1"},
		State:            &stateRef{Type: "completed", Name: "Done"},
	}

	got, ok := toIssue(n)
	if !ok {
		t.Fatalf("toIssue should not skip a fully populated completed issue")
	}

	want := Issue{
		Identifier:           "ENG-123",
		Title:                "Fix bug",
		Assignee:             "alice",
		Team:                 "ENG",
		ProjectID:            "proj-1",
		ProjectName:          "Q3",
		ProjectMilestoneID:   "ms-1",
		ProjectMilestoneName: "Milestone 1",
		StateType:            "completed",
		StateName:            "Done",
		CreatedAt:            createdAt,
		StartedAt:            startedAt,
		CompletedAt:          completedAt,
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

	got, ok := toIssue(n)
	if !ok {
		t.Fatalf("toIssue should not skip a completed issue with nil relations")
	}
	if got.Team != "" || got.ProjectID != "" || got.ProjectName != "" ||
		got.ProjectMilestoneID != "" || got.ProjectMilestoneName != "" ||
		got.StateType != "" || got.StateName != "" {
		t.Fatalf("toIssue() with nil relations = %+v, want all relation fields empty", got)
	}
}
