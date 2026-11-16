package session

import (
	"context"
	"errors"
	"strings"
	"time"
)

// PurgeChat represents a chat that may be eligible for purging.
type PurgeChat struct {
	ID        string
	ExpiresAt time.Time
}

// RestoredLookup provides a way to check if a chat has been restored.
type RestoredLookup interface {
	IsRestored(ctx context.Context, id string) (bool, error)
}

// PurgeStore is the interface for storing and managing purgeable chats.
type PurgeStore interface {
	RestoredLookup
	List(ctx context.Context) ([]PurgeChat, error)
	Delete(ctx context.Context, id string) error
}

// ErrInvalidPurgeInput is returned when purge input validation fails.
var ErrInvalidPurgeInput = errors.New("[D2908] invalid purge input")

// Purge removes expired chats from the store, skipping those that have been restored.
// It validates all inputs before performing any mutations.
func Purge(ctx context.Context, s PurgeStore, now time.Time) (deleted []string, err error) {
	if ctx == nil || s == nil || now.IsZero() {
		return nil, ErrInvalidPurgeInput
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	chats, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	if len(chats) == 0 {
		return nil, nil
	}

	// Validate: check for empty/whitespace-only IDs and duplicate exact IDs
	seen := make(map[string]bool)
	for _, c := range chats {
		if strings.TrimSpace(c.ID) == "" {
			return nil, ErrInvalidPurgeInput
		}
		if seen[c.ID] {
			return nil, ErrInvalidPurgeInput
		}
		seen[c.ID] = true
	}

	// Determine eligible chats: non-zero ExpiresAt and ExpiresAt <= now
	var eligible []PurgeChat
	for _, c := range chats {
		if c.ExpiresAt.IsZero() {
			continue
		}
		if !c.ExpiresAt.After(now) {
			eligible = append(eligible, c)
		}
	}

	if len(eligible) == 0 {
		return nil, nil
	}

	// Check each eligible chat: skip if restored
	for _, c := range eligible {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		restored, err := s.IsRestored(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		if restored {
			continue
		}

		err = s.Delete(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		deleted = append(deleted, c.ID)
	}

	if len(deleted) == 0 {
		return nil, nil
	}

	return deleted, nil
}
