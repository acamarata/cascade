package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePurgeStore is a test double implementing PurgeStore.
type fakePurgeStore struct {
	restored map[string]bool
	list     []PurgeChat
}

// IsRestored reports whether the given chat ID has been restored.
func (f *fakePurgeStore) IsRestored(_ context.Context, id string) (bool, error) {
	return f.restored[id], nil
}

// List returns the configured list of purge chats.
func (f *fakePurgeStore) List(_ context.Context) ([]PurgeChat, error) {
	return f.list, nil
}

// Delete is a no-op in the fake store.
func (f *fakePurgeStore) Delete(_ context.Context, _ string) error {
	return nil
}

// purgeTestCase holds inputs and expected results for a table-driven subtest.
type purgeTestCase struct {
	name        string
	chat        PurgeChat
	restored    map[string]bool
	wantDeleted []string
}

// purgeCases defines the table-driven test cases for TestPurgeSkipsRestoredChat.
var purgeCases = []purgeTestCase{
	{
		name: "expired_restored",
		chat: PurgeChat{
			ID:        "c1",
			ExpiresAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC).Add(-1 * time.Hour),
		},
		restored:    map[string]bool{"c1": true},
		wantDeleted: nil,
	},
	{
		name: "non_expired",
		chat: PurgeChat{
			ID:        "c2",
			ExpiresAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC).Add(1 * time.Hour),
		},
		restored:    map[string]bool{},
		wantDeleted: nil,
	},
	{
		name: "zero_expiry",
		chat: PurgeChat{
			ID:        "c3",
			ExpiresAt: time.Time{},
		},
		restored:    map[string]bool{},
		wantDeleted: nil,
	},
}

// TestPurgeSkipsRestoredChat verifies that Purge skips restored, non-expired, and zero-expiry chats.
func TestPurgeSkipsRestoredChat(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	for _, tc := range purgeCases {
		t.Run(tc.name, func(t *testing.T) {
			runPurgeSubtest(t, tc, now)
		})
	}
}

// runPurgeSubtest executes a single purge subtest case.
func runPurgeSubtest(t *testing.T, tc purgeTestCase, now time.Time) {
	t.Helper()
	store := &fakePurgeStore{
		restored: tc.restored,
		list:     []PurgeChat{tc.chat},
	}

	deleted, err := Purge(context.Background(), store, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tc.wantDeleted == nil && deleted != nil {
		t.Fatalf("expected nil deleted for %s, got %v", tc.name, deleted)
	}
}

func TestPurgeInvalidInput(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	// nil store (using context.TODO instead of nil to satisfy SA1012)
	_, err := Purge(context.TODO(), nil, now)
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
