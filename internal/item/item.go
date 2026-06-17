package item

import (
	"context"
	"time"
)

// Item is the vendor-neutral record produced by every Source and persisted by the store.
type Item struct {
	Source      string // "linear", "jira", ...
	Identifier  string // human id, e.g. "ENG-123" — unique within a source
	Title       string
	Assignee    string
	Team        string
	Project     string
	Status      string // plain string for now: "completed", "started", ...
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	UpdatedAt   time.Time // drives incremental fetch
}

// Source is implemented by each upstream tool (Linear, Jira, CSV, ...).
// since == zero means full fetch; otherwise only items updated after since.
type Source interface {
	Name() string
	Fetch(ctx context.Context, since time.Time) ([]Item, error)
}
