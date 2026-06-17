// Package sync provides the Sync orchestrator that wires an item.Source to a sqlite.Store.
package sync

import (
	"context"
	"fmt"

	"forecasting/internal/item"
	"forecasting/internal/sqlite"
)

// Sync fetches items from src that have changed since the store's latest watermark,
// then upserts them. Watermarks are keyed by src.Name(), so multiple sources never
// trample each other.
//
// A zero watermark (no items stored yet for this source) triggers a full fetch.
func Sync(ctx context.Context, src item.Source, store *sqlite.Store) (int, error) {
	since, err := store.LatestUpdatedAt(ctx, src.Name())
	if err != nil {
		return 0, fmt.Errorf("watermark: %w", err)
	}

	items, err := src.Fetch(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}

	if len(items) == 0 {
		return 0, nil
	}

	if err := store.Upsert(ctx, items...); err != nil {
		return 0, fmt.Errorf("upsert: %w", err)
	}

	return len(items), nil
}
