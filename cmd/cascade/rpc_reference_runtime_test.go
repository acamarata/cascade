//go:build !windows

package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

var rpcPageRow = regexp.MustCompile("(?m)^\\| `([^`]+)` \\| [A-Za-z]+ \\| (always|conditional|none) \\|")

func TestRPCMethodReference_MatchesComposedDaemon(t *testing.T) {
	page, err := os.ReadFile("../../docs/reference/rpc-methods.md")
	if err != nil {
		t.Fatal(err)
	}
	elevation := map[string]string{}
	for _, m := range rpcPageRow.FindAllStringSubmatch(string(page), -1) {
		elevation[m[1]] = m[2]
	}
	if len(elevation) < 40 {
		t.Fatalf("page lists %d methods, want >= 40", len(elevation))
	}
	deps := policyDeps(t, "")
	var reg *rpc.Registry
	_, cleanups, err := composeDaemon(t.Context(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { reg = r })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, m := range reg.Methods() {
		served[m] = true
		level, ok := elevation[m]
		if !ok {
			t.Errorf("composed daemon serves %q; the page has no row", m)
			continue
		}
		if (level != "none") != rpc.IsElevatedVerb(m) {
			t.Errorf("%s: page elevation %q disagrees with rpc.IsElevatedVerb", m, level)
		}
	}
	for m := range elevation {
		if !served[m] {
			t.Errorf("page lists %q; the composed daemon does not serve it", m)
		}
	}
}
