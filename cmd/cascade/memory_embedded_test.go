// Purpose: DEFECT-cli-surfaces-promise-embedded-mode.md's regression tests
// for memory_embedded.go: proves memoryRoute takes the embedded path (and
// never touches the injected client seam) when no daemon is confirmed
// live, proves it takes the client path (and never touches disk) when one
// is, and proves memoryCallEmbedded's composition actually round-trips a
// real record through all four memory.* verbs — the same behaviour a live
// daemon's memory.Handler gives, per this file's own header comment.
//
// SPORT: cmd.cascade.cmd.memory (TEST, embedded-path routing).
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// poisonMemoryCall fails the test if the client seam is ever invoked. It
// is the mutation-proof target for memoryRoute's guard: reverting the
// guard to call memoryCall unconditionally makes every case in
// TestMemoryRouteEmbeddedWhenDaemonless fail here instead of silently
// answering wrong.
func poisonMemoryCall(t *testing.T) memoryCallFunc {
	t.Helper()
	return func(context.Context, string, string, any, any) error {
		t.Fatal("memoryRoute dialed the daemon client seam in daemonless mode")
		return nil
	}
}

// TestMemoryRouteEmbeddedWhenDaemonless proves memoryRoute answers
// `memory remember` from the embedded memory.Handler, never the injected
// Call seam, both when the probe explicitly confirmed no daemon and when
// the probe state is undecidable (ok=false) — memoryRoute's own doc
// comment states the second case defaults to embedded, mirroring
// recallQuery's identical rule.
func TestMemoryRouteEmbeddedWhenDaemonless(t *testing.T) {
	cases := map[string]context.Context{
		"confirmed no daemon":    runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true}),
		"undecidable (ok=false)": context.Background(),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			deps := memoryDeps{Paths: fakeMemoryPaths{root: root}, Call: poisonMemoryCall(t)}
			cmd := &cobra.Command{}
			cmd.SetContext(ctx)

			params := memory.RememberParams{Content: "embedded body", Type: "project", Name: "embedded-note"}
			var result memory.RememberResult
			if err := memoryRoute(cmd, deps, memory.MethodRemember, params, &result); err != nil {
				t.Fatalf("memoryRoute remember: %v", err)
			}
			if result.ID != "project/embedded-note" {
				t.Fatalf("result.ID = %q, want project/embedded-note", result.ID)
			}

			recordPath := filepath.Join(memoryStoreDir(deps.Paths), "project", "embedded-note.md")
			if _, err := os.Stat(recordPath); err != nil {
				t.Fatalf("embedded remember did not write %s: %v", recordPath, err)
			}
		})
	}
}

// TestMemoryRouteClientWhenDaemonLive proves the opposite branch: when the
// probe confirms a live daemon, memoryRoute calls the injected Call seam
// and never touches the embedded store on disk.
func TestMemoryRouteClientWhenDaemonLive(t *testing.T) {
	root := t.TempDir()
	var invoked bool
	deps := memoryDeps{
		Paths: fakeMemoryPaths{root: root},
		Call: func(_ context.Context, _, method string, _, out any) error {
			invoked = true
			if method != memory.MethodRemember {
				t.Errorf("method = %q, want memory.remember", method)
			}
			raw, err := json.Marshal(memory.RememberResult{ID: "project/from-daemon"})
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, out)
		},
	}
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)

	params := memory.RememberParams{Content: "client body", Name: "client-note"}
	var result memory.RememberResult
	if err := memoryRoute(cmd, deps, memory.MethodRemember, params, &result); err != nil {
		t.Fatalf("memoryRoute remember: %v", err)
	}
	if !invoked {
		t.Fatal("memoryRoute did not call the injected client seam with a live daemon")
	}
	if result.ID != "project/from-daemon" {
		t.Fatalf("result.ID = %q, want project/from-daemon (from the client seam)", result.ID)
	}
	if _, err := os.Stat(filepath.Join(memoryStoreDir(deps.Paths), "project", "client-note.md")); !os.IsNotExist(err) {
		t.Fatalf("memoryRoute wrote to the embedded store while the client path was taken: stat err=%v", err)
	}
}

// TestMemoryCallEmbedded_RoundTripsAllFourMethods drives remember, recall,
// list and forget through the SAME embedded composition
// memoryCallEmbedded builds, proving it behaves like the real
// memory.Handler the daemon serves (internal/memory/rpc.go), not a second,
// hand-rolled implementation.
func TestMemoryCallEmbedded_RoundTripsAllFourMethods(t *testing.T) {
	deps := memoryDeps{Paths: fakeMemoryPaths{root: t.TempDir()}}
	ctx := context.Background()

	var remembered memory.RememberResult
	if err := memoryCallEmbedded(ctx, deps, memory.MethodRemember,
		memory.RememberParams{Content: "roundtrip body", Name: "roundtrip"}, &remembered); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if remembered.ID != "project/roundtrip" {
		t.Fatalf("remembered.ID = %q, want project/roundtrip", remembered.ID)
	}

	var recalled memory.RecallResult
	if err := memoryCallEmbedded(ctx, deps, memory.MethodRecall,
		memory.RecallParams{Query: "roundtrip", K: 10}, &recalled); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(recalled.Units) != 1 || memory.Address(recalled.Units[0].Kind, recalled.Units[0].Name) != "project/roundtrip" {
		t.Fatalf("recalled.Units = %+v, want exactly project/roundtrip", recalled.Units)
	}

	var listed memory.ListResult
	if err := memoryCallEmbedded(ctx, deps, memory.MethodList, memory.ListParams{}, &listed); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Units) != 1 || memory.Address(listed.Units[0].Kind, listed.Units[0].Name) != "project/roundtrip" {
		t.Fatalf("listed.Units = %+v, want exactly project/roundtrip", listed.Units)
	}

	var forgotten memory.ForgetResult
	if err := memoryCallEmbedded(ctx, deps, memory.MethodForget,
		memory.ForgetParams{ID: "project/roundtrip", Reason: "test cleanup"}, &forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !forgotten.Forgotten || forgotten.ID != "project/roundtrip" {
		t.Fatalf("forgotten = %+v, want Forgotten=true for project/roundtrip", forgotten)
	}
	// The daemon's own registerMemoryHandler wires no index (its comment:
	// "no projection job runs"), and this embedded composition mirrors
	// that exactly, so the trace must say the same: no row, posting or
	// vector claimed as scrubbed, never a scrub that never ran.
	if forgotten.Index.Row || forgotten.Index.Postings != 0 || forgotten.Index.Vector || forgotten.Index.VectorProbed {
		t.Fatalf("forgotten.Index = %+v, want no scrub claimed (no index wired)", forgotten.Index)
	}
}

// TestMemoryCallEmbedded_UnknownMethodRefuses proves the method-name guard
// in memoryEmbeddedMethods: a namespace this composition does not serve
// (e.g. soul.get) refuses with KindUnsupported rather than a nil-map panic.
func TestMemoryCallEmbedded_UnknownMethodRefuses(t *testing.T) {
	deps := memoryDeps{Paths: fakeMemoryPaths{root: t.TempDir()}}
	var out map[string]any
	err := memoryCallEmbedded(context.Background(), deps, "soul.get", struct{}{}, &out)
	if err == nil {
		t.Fatal("memoryCallEmbedded: want an error for an unserved method, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("err = %v, want KindUnsupported", err)
	}
}
