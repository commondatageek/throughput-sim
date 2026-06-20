package linear

import "time"

// Issue is the record fetched from Linear and persisted by the store.
type Issue struct {
	Identifier           string // e.g. "ENG-123"
	Title                string
	Assignee             string
	Team                 string
	ProjectID            string
	ProjectName          string
	ProjectMilestoneID   string
	ProjectMilestoneName string
	StateType            string // raw workflow state type, e.g. Linear's state.type
	StateName            string // human-readable workflow state name, e.g. Linear's state.name
	CreatedAt            time.Time
	StartedAt            time.Time
	CompletedAt          time.Time
	ArchivedAt           time.Time
	AutoArchivedAt       time.Time
	AddedToProjectAt     time.Time
	UpdatedAt            time.Time // drives incremental fetch
}
