// Purpose (this file): APPLY-mode handshake tests over the REAL
//
//	internal/runtime ConfigWriter.ApplyDiff (P1-E25-W5-S103-T1), so the
//	user-set skip and the idempotent second run are the writer's own
//	classification, not a result a test double was told to return.
//
// Constraints: config.toml lives under t.TempDir(); no nself binary is
//
//	forked (activeRunner is scripted). A test file may import
//	internal/runtime (the Art.10.2 ban covers non-test files, and
//	internal/runtime imports nothing under plugins/, so there is no cycle).
//
// SPORT: plugins/nself handshake (TEST) — P1-E25-W5-S103-T1.

package nself

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// writerApplier adapts runtime.ConfigWriter to ConfigApplier the same way
// the composition root's applier does (entry-for-entry, pinned owner).
type writerApplier struct{ path string }

func (a writerApplier) ApplyDiff(_ context.Context, _ string, entries []ConfigEntry) (ConfigResult, error) {
	diff := runtime.ConfigDiff{Owner: handshakeOwner}
	for _, e := range entries {
		diff.Entries = append(diff.Entries, runtime.DiffEntry{Path: e.Path, Literal: e.Literal})
	}
	res, err := (&runtime.ConfigWriter{Path: a.path}).ApplyDiff(diff)
	if err != nil {
		return ConfigResult{}, err
	}
	convert := func(in []runtime.DiffOutcome) []ConfigOutcome {
		out := make([]ConfigOutcome, 0, len(in))
		for _, o := range in {
			out = append(out, ConfigOutcome{Path: o.Path, Reason: o.Reason})
		}
		return out
	}
	return ConfigResult{Applied: convert(res.Applied), Unchanged: convert(res.Unchanged), Skipped: convert(res.Skipped)}, nil
}

// applyFixture scripts a full project, sets every env-ref, and binds a
// real writer over a config.toml seeded with seed. It returns the path.
func applyFixture(t *testing.T, seed string) (string, string) {
	t.Helper()
	dir := nselfProjectDir(t)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if seed != "" {
		if err := os.WriteFile(cfg, []byte(seed), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	origRunner, origApplier := activeRunner, configApplier
	activeRunner, configApplier = fullScript(), writerApplier{path: cfg}
	t.Cleanup(func() { activeRunner, configApplier = origRunner, origApplier })
	withGetenv(t, true, "")
	return dir, cfg
}

func TestHandshakeRespectsUserProfile(t *testing.T) {
	const seed = "[runtime]\nprofile = \"local\"\n"
	dir, cfg := applyFixture(t, seed)

	resp, err := runHandshake(context.Background(), handshakeModeApply, dir)
	if err != nil {
		t.Fatalf("runHandshake: %v", err)
	}
	if resp.Note != userSetProfileNote {
		t.Fatalf("note = %q, want the user-set note", resp.Note)
	}
	if resp.RestartRequired {
		t.Fatal("restart_required = true, want false (runtime.profile was not applied)")
	}
	tree, err := runtime.DecodeConfigFile(cfg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := tree["runtime"].(map[string]interface{})["profile"]; got != "local" {
		t.Fatalf("runtime.profile = %v, want the user's \"local\" left alone", got)
	}
}

func TestHandshakeSecondRunUnchanged(t *testing.T) {
	dir, cfg := applyFixture(t, "")

	first, err := runHandshake(context.Background(), handshakeModeApply, dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != "applied" || !first.RestartRequired {
		t.Fatalf("first run = %+v, want applied with restart_required", first)
	}
	before, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	second, err := runHandshake(context.Background(), handshakeModeApply, dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Status != "unchanged" || len(second.Applied) != 0 || len(second.Unchanged) != len(first.Applied) {
		t.Fatalf("second run = %+v, want every entry unchanged", second)
	}
	after, err := os.ReadFile(cfg)
	if err != nil || string(after) != string(before) {
		t.Fatalf("the second run rewrote config.toml (%v)", err)
	}
}

// Missing env references remain visible even when descriptor writes succeed.
func TestHandshakeAppliedDescriptorsStillPendingEnv(t *testing.T) {
	dir, cfg := applyFixture(t, "")
	withGetenv(t, false, "")
	resp, err := runHandshake(context.Background(), handshakeModeApply, dir)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "pending-env" || len(resp.Applied) == 0 || len(resp.MissingEnv) != 6 {
		t.Fatalf("pending env after descriptor apply: %+v", resp)
	}
	tree, err := runtime.DecodeConfigFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := tree["runtime"]; exists {
		t.Fatal("runtime profile written with unresolved env refs")
	}
}
