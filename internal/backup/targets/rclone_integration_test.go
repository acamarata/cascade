//go:build integration

// Purpose: TestTargetRcloneRealBinary — the Art.2 real counterpart for
// RcloneTarget: the real installed rclone binary (execRcloneRunner, via
// a nil runner) against rclone's own "local" backend, a real rclone
// remote type per this ticket's HOW ("rclone's own local backend is a
// real rclone remote type"). Skips (never fakes a pass) when rclone is
// not on PATH.
//
// SPORT: internal.backup.targets.rclone/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/backup/targets"
)

func TestTargetRcloneRealBinary(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not on PATH; skipping real-binary run")
	}
	remote := t.TempDir()
	ctx := context.Background()
	tgt, err := targets.NewRcloneTarget(remote, nil, testEgressEngine(t))
	if err != nil {
		t.Fatalf("NewRcloneTarget: %v", err)
	}

	want := "real rclone local-backend round trip"
	if err := tgt.Put(ctx, "objects/ab/roundtrip", strings.NewReader(want)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := tgt.Get(ctx, "objects/ab/roundtrip")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != want {
		t.Fatalf("real rclone round trip = %q, want %q", got, want)
	}

	keys, err := tgt.List(ctx, "objects/ab/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == "objects/ab/roundtrip" {
			found = true
		}
	}
	if !found {
		t.Fatalf("List(objects/ab/) = %v, want to contain objects/ab/roundtrip", keys)
	}

	if err := tgt.Delete(ctx, "objects/ab/roundtrip"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tgt.Get(ctx, "objects/ab/roundtrip"); err == nil {
		t.Fatal("Get after Delete against real rclone returned nil error")
	}
	// A second Delete of the same, now-absent key must not error.
	if err := tgt.Delete(ctx, "objects/ab/roundtrip"); err != nil {
		t.Fatalf("Delete(already-absent) = %v, want nil", err)
	}
}
