package ingest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestDeclarationsExist asserts the declarations exist and that Ingest is
// still a stub returning ErrNotImplemented.
func TestDeclarationsExist(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented is nil")
	}
	var row TicketRow
	if row.Key != "" {
		t.Fatalf("zero TicketRow.Key = %q, want empty string", row.Key)
	}
	if err := Ingest(nil); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Ingest error = %v, want ErrNotImplemented", err)
	}
}

// planTree builds base/hq (a phases folder), base/.ai (a legacy current.yaml)
// and base/other (not a plan) and returns base.
func planTree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	mustWrite(t, filepath.Join(base, "hq", "phases", "PHASE.md"), "# phase\n")
	mustWrite(t, filepath.Join(base, ".ai", "current.yaml"), "phase: p1\n")
	mustWrite(t, filepath.Join(base, "other", "notes.md"), "x\n")
	return base
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// TestDiscoveryFindsHqAndDotAi covers default names, explicit roots, the env
// value and an invalid configured root.
func TestDiscoveryFindsHqAndDotAi(t *testing.T) {
	base := planTree(t)
	hq, ai := filepath.Join(base, "hq"), filepath.Join(base, ".ai")
	want := sorted([]string{hq, ai})

	got, err := DiscoverPlanRoots(Config{Base: base})
	if err != nil || !reflect.DeepEqual(sorted(got), want) {
		t.Fatalf("default roots = %v, %v; want %v", got, err, want)
	}

	got, err = DiscoverPlanRoots(Config{Base: base, Roots: []string{hq}})
	if err != nil || !reflect.DeepEqual(got, []string{hq}) {
		t.Fatalf("explicit roots = %v, %v; want [%s]", got, err, hq)
	}

	env := func(k string) string {
		if k == "CAPP_PLAN_ROOTS" {
			return ai
		}
		return ""
	}
	got, err = DiscoverPlanRoots(Config{Base: base, Getenv: env})
	if err != nil || !reflect.DeepEqual(got, []string{ai}) {
		t.Fatalf("env roots = %v, %v; want [%s]", got, err, ai)
	}

	missing := filepath.Join(base, "gone")
	if _, err := DiscoverPlanRoots(Config{Roots: []string{missing}}); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("missing explicit root error = %v, want ErrInvalidRoot", err)
	}
	if _, err := DiscoverPlanRoots(Config{Roots: []string{filepath.Join(base, "other")}}); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("non-plan explicit root error = %v, want ErrInvalidRoot", err)
	}
}
