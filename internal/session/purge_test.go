package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePurgeStore struct {
	restored map[string]bool
	list     []PurgeChat
}

func (f *fakePurgeStore) IsRestored(ctx context.Context, id string) (bool, error) {
	return f.restored[id], nil
}

func (f *fakePurgeStore) List(ctx context.Context) ([]PurgeChat, error) {
	return f.list, nil
}

func (f *fakePurgeStore) Delete(ctx context.Context, id string) error {
	return nil
}

func TestPurgeSkipsRestoredChat(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	// Expired chat that is restored - should be skipped
	expiredRestored := PurgeChat{
		ID:        "c1",
		ExpiresAt: now.Add(-1 * time.Hour),
	}

	store := &fakePurgeStore{
		restored: map[string]bool{"c1": true},
		list:     []PurgeChat{expiredRestored},
	}

	deleted, err := Purge(context.Background(), store, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected nil deleted for restored expired chat, got %v", deleted)
	}

	// Non-expired chat - should not perform lookup or deletion
	nonExpired := PurgeChat{
		ID:        "c2",
		ExpiresAt: now.Add(1 * time.Hour),
	}
	store2 := &fakePurgeStore{
		restored: map[string]bool{},
		list:     []PurgeChat{nonExpired},
	}

	deleted2, err := Purge(context.Background(), store2, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted2 != nil {
		t.Fatalf("expected nil deleted for non-expired chat, got %v", deleted2)
	}

	// Zero expiry chat - should not perform lookup or deletion
	zeroExpiry := PurgeChat{
		ID:        "c3",
		ExpiresAt: time.Time{},
	}
	store3 := &fakePurgeStore{
		restored: map[string]bool{},
		list:     []PurgeChat{zeroExpiry},
	}

	deleted3, err := Purge(context.Background(), store3, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted3 != nil {
		t.Fatalf("expected nil deleted for zero-expiry chat, got %v", deleted3)
	}
}

func TestPurgeInvalidInput(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	// nil context
	_, err := Purge(nil, nil, now)
	if !errors.Is(err, ErrInvalidPurgeInput) {
		t.Fatalf("expected ErrInvalidPurgeInput for nil ctx, got %v", err)
	}

	// nil store
	_, err = Purge(context.Background(), nil, now)
	if !errors.Is(err, ErrInvalidPurgeInput) {
		t.Fatalf("expected ErrInvalidPurgeInput for nil store, got %v", err)
	}

	// zero time
	_, err = Purge(context.Background(), &fakePurgeStore{}, time.Time{})
	if !errors.Is(err, ErrInvalidPurgeInput) {
		t.Fatalf("expected ErrInvalidPurgeInput for zero time, got %v", err)
	}
}

func TestPurgeEmptyListing(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	store := &fakePurgeStore{
		list: []PurgeChat{},
	}

	deleted, err := Purge(context.Background(), store, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected nil deleted for empty listing, got %v", deleted)
	}
}
