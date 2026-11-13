package promise

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSameKeySamePromise(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	p1 := &Promise{
		ID:             "1",
		Kind:           "job",
		IdempotencyKey: "key-1",
		State:          Pending,
	}
	got1, err := s.Submit(ctx, p1)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if got1.ID != "1" {
		t.Fatalf("expected ID 1, got %s", got1.ID)
	}

	p2 := &Promise{
		ID:             "2",
		Kind:           "job",
		IdempotencyKey: "key-1",
		State:          Pending,
	}
	got2, err := s.Submit(ctx, p2)
	if err != nil {
		t.Fatalf("Submit duplicate failed: %v", err)
	}
	if got2.ID != "1" {
		t.Fatalf("expected duplicate to return original ID 1, got %s", got2.ID)
	}
}

func TestGetUnknownID(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	_, err := s.Get(ctx, "unknown")
	if err == nil {
		t.Fatal("expected error for unknown ID, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdatePersistsStateAndHeartbeat(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	p := &Promise{
		ID:             "1",
		Kind:           "job",
		IdempotencyKey: "key-1",
		State:          Pending,
	}
	_, err := s.Submit(ctx, p)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	now := time.Now()
	p.State = Running
	p.Heartbeat = now
	err = s.Update(ctx, p)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got, err := s.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.State != Running {
		t.Fatalf("expected state Running, got %s", got.State)
	}
	if !got.Heartbeat.Equal(now) {
		t.Fatalf("expected heartbeat %v, got %v", now, got.Heartbeat)
	}
}

func TestListStale(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now()
	p1 := &Promise{
		ID:             "1",
		IdempotencyKey: "key-1",
		NextAt:         now.Add(-time.Hour),
	}
	p2 := &Promise{
		ID:             "2",
		IdempotencyKey: "key-2",
		NextAt:         now.Add(time.Hour),
	}
	s.Submit(ctx, p1)
	s.Submit(ctx, p2)

	stale, err := s.ListStale(ctx, now)
	if err != nil {
		t.Fatalf("ListStale failed: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale promise, got %d", len(stale))
	}
	if stale[0].ID != "1" {
		t.Fatalf("expected stale ID 1, got %s", stale[0].ID)
	}
}
