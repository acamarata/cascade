package main

// Purpose: DEFECT-recall-no-embedded-path.md's regression test: drive the
//
//	REAL production command tree (newRootCmd with productionRecallDeps, not
//	a fake-Paths stub) against a CASCADE_HOME that has never been created —
//	the exact virgin-HOME state the Wave-2 hardening gate hit on the
//	shipped binary ("warning: daemon not running; running in embedded
//	(daemonless) mode" immediately followed by a socket dial anyway).
//	Reuses execRootProductionVirginHome (context_slice_virgin_home_test.go)
//	verbatim: same helper, same CASCADE_HOME/CASCADE_SOCKET/HOME shape, so
//	the daemonless probe always takes the embedded path deterministically
//	here too.
//
// SPORT: cmd.cascade.cmd.recall (FIX, embedded-path routing test).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRecallVirginHomeNoIndexNeverDials is the primary regression proof:
// on a virgin HOME with no daemon and no retrieval index ever built,
// `cascade recall` must answer with the catalog's own honest refusal
// (KindNotFound: "no retrieval index has been built yet") — the SAME
// refusal a live daemon with no index gives (recall_integration_test.go's
// TestRecallIsReachableOnTheDaemonTheCompositionRootBuilds) — never the
// dial failure the shipped binary produced.
func TestRecallVirginHomeNoIndexNeverDials(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	if _, err := os.Stat(virginHome); !os.IsNotExist(err) {
		t.Fatalf("test setup bug: virginHome must not exist yet, stat err=%v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "recall", "gofmt")
	if err == nil {
		t.Fatalf("cascade recall on a virgin HOME with no index: got nil error, "+
			"want the catalog's not-found refusal\noutput:\n%s", got)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound (\"no retrieval index has been built yet\")", err)
	}
	if strings.Contains(err.Error(), "daemon not running") || strings.Contains(err.Error(), "dial unix") {
		t.Fatalf("err = %v, still the dial-error shape DEFECT-recall-no-embedded-path.md reported", err)
	}

	indexDir := filepath.Join(virginHome, "data", "retrieval")
	if info, statErr := os.Stat(indexDir); statErr != nil || !info.IsDir() {
		t.Errorf("cascade recall did not bootstrap %s: stat err=%v", indexDir, statErr)
	}
}

// TestRecallVirginHomeEmptyIndexNoLegAvailable proves the embedded path
// runs the REAL recall.Service.Query fusion logic end to end, rather than
// short-circuiting on "no index" alone: with a genuinely built (if empty)
// catalog present, the answer changes to the leg-availability refusal
// (KindUnavailable — this build wires no full-text or embedding leg,
// registerRecallHandler's own doc comment in daemon_unix_handlers.go), the
// identical answer a live daemon serving the same empty catalog would give.
// Still never a dial error either way.
func TestRecallVirginHomeEmptyIndexNoLegAvailable(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	indexDir := filepath.Join(virginHome, "data", "retrieval")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatalf("test setup: mkdir %s: %v", indexDir, err)
	}
	doc := map[string]any{"version": 1, "corpora": []any{}, "records": []any{}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("test setup: marshal catalog fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, "catalog.json"), raw, 0o600); err != nil {
		t.Fatalf("test setup: write catalog fixture: %v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "recall", "gofmt")
	if err == nil {
		t.Fatalf("cascade recall against an empty index with no leg configured: got nil error, "+
			"want the leg-unavailable refusal\noutput:\n%s", got)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable (\"no retrieval leg is available\")", err)
	}
	if strings.Contains(err.Error(), "daemon not running") || strings.Contains(err.Error(), "dial unix") {
		t.Fatalf("err = %v, still a dial-error shape, not the catalog/leg refusal", err)
	}
}
