package background

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWatchdogRunsRecordedCrunch(t *testing.T) {
	dir := t.TempDir()
	called := false
	exec := func(ctx context.Context, cmd string) error {
		called = true
		return nil
	}
	spec := WatchdogSpec{Crunch: "capp gc crunch"}
	res, err := RunWatchdog(context.Background(), spec, "capp gc crunch", dir, exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "executed" {
		t.Fatalf("expected executed, got %s", res)
	}
	if !called {
		t.Fatal("exec was not called")
	}
}

func TestWatchdogRejectsUnrecordedCrunch(t *testing.T) {
	dir := t.TempDir()
	exec := func(ctx context.Context, cmd string) error {
		t.Fatal("exec should not be called for unrecorded crunch")
		return nil
	}
	spec := WatchdogSpec{Crunch: "capp gc crunch"}
	_, err := RunWatchdog(context.Background(), spec, "other crunch", dir, exec)
	if !errors.Is(err, ErrUnrecorded) {
		t.Fatalf("expected ErrUnrecorded, got %v", err)
	}
}

func TestWatchdogHonoursStopMarker(t *testing.T) {
	dir := t.TempDir()
	stopPath := filepath.Join(dir, "STOP")
	if err := os.WriteFile(stopPath, []byte("stop"), 0644); err != nil {
		t.Fatal(err)
	}
	called := false
	exec := func(ctx context.Context, cmd string) error {
		called = true
		return nil
	}
	spec := WatchdogSpec{Crunch: "capp gc crunch"}
	res, err := RunWatchdog(context.Background(), spec, "capp gc crunch", dir, exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "halted" {
		t.Fatalf("expected halted, got %s", res)
	}
	if called {
		t.Fatal("exec should not be called when STOP marker is present")
	}
}

func TestWatchdogHaltsOnStopBeforeCrunch(t *testing.T) {
	dir := t.TempDir()
	stopPath := filepath.Join(dir, "STOP")
	if err := os.WriteFile(stopPath, []byte("stop"), 0644); err != nil {
		t.Fatal(err)
	}
	called := false
	exec := func(ctx context.Context, cmd string) error {
		called = true
		return nil
	}
	spec := WatchdogSpec{Crunch: "capp gc crunch"}
	res, err := RunWatchdog(context.Background(), spec, "capp gc crunch", dir, exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "halted" {
		t.Fatalf("expected halted, got %s", res)
	}
	if called {
		t.Fatal("exec should not be called when STOP exists before crunch")
	}
}
