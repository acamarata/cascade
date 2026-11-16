package background

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestKeeperPassRunsWhenFree(t *testing.T) {
	dir := t.TempDir()
	var called int
	run := func() error {
		called++
		return nil
	}
	var out bytes.Buffer
	code := RunKeeperPass(dir, &out, run)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if called != 1 {
		t.Fatalf("expected run called 1 time, got %d", called)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "keeper.lock")); !os.IsNotExist(err) {
		t.Fatalf("expected lock to be removed, stat err: %v", err)
	}
}

func TestKeeperPassSkipsWhenLocked(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "keeper.lock")

	origPidAlive := pidAlive
	defer func() { pidAlive = origPidAlive }()
	pidAlive = func(pid int) bool { return true }

	if err := os.WriteFile(lockPath, []byte("1234\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var called int
	run := func() error {
		called++
		return nil
	}
	var out bytes.Buffer
	code := RunKeeperPass(dir, &out, run)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if called != 0 {
		t.Fatalf("expected run called 0 times, got %d", called)
	}
	if !bytes.Contains(out.Bytes(), []byte("keeper pass skip: another pass running")) {
		t.Fatalf("expected skip message, got %q", out.String())
	}
}

func TestKeeperPassStaleLockTakenOver(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "keeper.lock")

	if err := os.WriteFile(lockPath, []byte("2147483647\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var called int
	run := func() error {
		called++
		return nil
	}
	var out bytes.Buffer
	code := RunKeeperPass(dir, &out, run)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if called != 1 {
		t.Fatalf("expected run called 1 time, got %d", called)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "keeper.lock")); !os.IsNotExist(err) {
		t.Fatalf("expected lock to be removed, stat err: %v", err)
	}
}

func TestKeeperPassErrorReturnsOne(t *testing.T) {
	dir := t.TempDir()
	runErr := errors.New("run failed")
	var called int
	run := func() error {
		called++
		return runErr
	}
	var out bytes.Buffer
	code := RunKeeperPass(dir, &out, run)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if called != 1 {
		t.Fatalf("expected run called 1 time, got %d", called)
	}
	if _, err := os.Stat(filepath.Join(dir, "keeper.lock")); !os.IsNotExist(err) {
		t.Fatalf("expected lock to be removed, stat err: %v", err)
	}
}
