// Package background provides manager-routed spawn operations.
package background

import (
	"errors"
	"testing"
)

// TestSpawnViaManagerCLI verifies that spawning via the manager CLI succeeds.
func TestSpawnViaManagerCLI(t *testing.T) {
	req := SpawnRequest{}
	res, err := Spawn(true, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OK {
		t.Fatal("expected OK=true")
	}
	if !res.ViaManager {
		t.Fatal("expected ViaManager=true")
	}
}

// TestSpawnUnderID verifies that spawning with --under flag sets parent ID.
func TestSpawnUnderID(t *testing.T) {
	req := SpawnRequest{UnderID: "42"}
	res, err := Spawn(true, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ParentID != "42" {
		t.Fatalf("expected ParentID=42, got %q", res.ParentID)
	}
}

// TestSpawnWithModel verifies that spawning with --model flag sets model name.
func TestSpawnWithModel(t *testing.T) {
	req := SpawnRequest{Model: "llama"}
	res, err := Spawn(true, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "llama" {
		t.Fatalf("expected Model=llama, got %q", res.Model)
	}
}

// TestUIButtonsRemoved verifies that UI config has no spawn buttons.
func TestUIButtonsRemoved(t *testing.T) {
	cfg := DefaultUIConfig()
	if len(cfg.SpawnButtons) != 0 {
		t.Fatalf("expected 0 spawn buttons, got %d", len(cfg.SpawnButtons))
	}
}

// TestSpawnWithoutManagerFails verifies that spawning without manager returns D6148 error.
func TestSpawnWithoutManagerFails(t *testing.T) {
	req := SpawnRequest{}
	_, err := Spawn(false, req)
	if !errors.Is(err, ErrNoManager) {
		t.Fatalf("expected ErrNoManager, got %v", err)
	}
}
