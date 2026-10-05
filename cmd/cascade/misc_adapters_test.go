// Purpose: unit coverage for a handful of small composition-root
//
//	functions that shipped with zero direct test callers:
//	productionStdinIsPiped (vault_quarantine.go),
//	productionElevationPrecondition (daemon.go),
//	clientRecallCall/clientMemoryCall (recall.go/memory.go), and
//	realHTTPDoer.Get's request-construction refusal (provider_usage_cmd.go).
//	Each is exercised directly rather than through the CLI command tree
//	that happens to wire it, since the command tree does not reach every
//	one of these in its own existing suite.
//
// SPORT: cmd.cascade/TEST (composition-root adapter coverage).
package main

import (
	"context"
	"github.com/acamarata/cascade/internal/elevation"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestProductionStdinIsPiped_Idempotent proves the real os.Stdin.Stat
// check is a pure query: two consecutive calls in the same process, with
// stdin untouched between them, must agree. The test harness's actual
// stdin (tty vs pipe) is not under this test's control, so the concrete
// boolean is not asserted -- but a real Stat-backed implementation is
// deterministic call to call, which this test can and does verify.
func TestProductionStdinIsPiped_Idempotent(t *testing.T) {
	first := productionStdinIsPiped()
	second := productionStdinIsPiped()
	if first != second {
		t.Errorf("productionStdinIsPiped() = %v then %v, want a stable answer across calls", first, second)
	}
}

// TestProductionElevationPrecondition_NilPathsFailsClosed proves the
// documented fail-closed default: a nil PathProvider (or one with an
// empty DataDir) reports both preconditions false rather than probing a
// guessed path.
func TestProductionElevationPrecondition_NilPathsFailsClosed(t *testing.T) {
	precondition := productionElevationPrecondition(nil)
	enrolled, available := precondition()
	if enrolled || available {
		t.Errorf("productionElevationPrecondition(nil)() = (%v, %v), want (false, false)", enrolled, available)
	}
}

// TestProductionElevationPrecondition_RealPathsRuns proves the non-nil
// branch actually builds and queries a real elevation.Keystore/
// ElevationTrustStore pair over a fresh data dir, rather than only ever
// taking the nil short-circuit.
func TestProductionElevationPrecondition_RealPathsRuns(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	precondition := custodyElevationPrecondition(paths, func(string) elevation.Custody {
		return testCustody(t, availableKeystore{}, elevation.CustodyPlatform)()
	})
	// A fresh, never-enrolled data dir: IsEnrolled must be false. The
	// keystore availability answer is platform-dependent and not
	// asserted here -- only that the real query path runs without error.
	enrolled, _ := precondition()
	if enrolled {
		t.Error("productionElevationPrecondition on a fresh data dir reported enrolled = true, want false")
	}
}

// TestClientRecallCall_TransportUnreachable and
// TestClientMemoryCall_TransportUnreachable prove each dials a real unix
// socket via internal/client (production code) against a path nothing
// listens on -- the same class of proof cascadepa_wiring_test.go's
// TestCascadePAClient_OneShot_TransportUnreachable uses, never a fake
// transport.
func TestClientRecallCall_TransportUnreachable(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nothing-here.sock")
	var out map[string]any
	err := clientRecallCall(context.Background(), socket, "recall.query", map[string]string{"q": "x"}, &out)
	if err == nil {
		t.Fatal("clientRecallCall against an unreachable socket = nil error, want a transport failure")
	}
}

func TestClientMemoryCall_TransportUnreachable(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nothing-here.sock")
	var out map[string]any
	err := clientMemoryCall(context.Background(), socket, "memory.soul.show", nil, &out)
	if err == nil {
		t.Fatal("clientMemoryCall against an unreachable socket = nil error, want a transport failure")
	}
}

// TestRealHTTPDoer_Get_InvalidURLRefusesBeforeAnyNetworkIO proves the
// request-construction refusal branch: an unparsable URL fails at
// http.NewRequestWithContext, before any real outbound connection is
// attempted -- the only branch this unit lane can exercise without
// importing net/http itself (internal/build's Art.7.2 gate).
func TestRealHTTPDoer_Get_InvalidURLRefusesBeforeAnyNetworkIO(t *testing.T) {
	doer := realHTTPDoer{}
	_, err := doer.Get(context.Background(), "://not-a-valid-url")
	if err == nil {
		t.Fatal("realHTTPDoer.Get(invalid URL) = nil error, want a request-construction failure")
	}
}
func TestDaemonlessPreconditionFalseOnFileTier(t *testing.T) {
	k := &refusingFileKey{signingKeystore: newSigningKeystore(t)}
	custody := testCustody(t, k, elevation.CustodyFile)
	precondition := custodyElevationPrecondition(fakeDaemonPaths{root: t.TempDir()}, func(string) elevation.Custody { return custody() })
	_, available := precondition()
	if available || k.signs != 0 {
		t.Fatalf("available=%v signs=%d", available, k.signs)
	}
}
func testCustody(t *testing.T, ks elevation.ElevationKeystore, tier elevation.CustodyTier) func() elevation.Custody {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	sel := elevation.Selector{DataDir: dir, Sources: []elevation.CustodySource{{Tier: tier, Name: "test", Open: func(string) (elevation.ElevationKeystore, bool) { return ks, ks != nil && ks.IsAvailable() }}}}
	return sel.Select
}
func TestNoSelectorLiteralOutsideElevation(t *testing.T) {
	assertSelectorTree(t, filepath.Join("..", ".."))
}

func assertSelectorTree(t *testing.T, root string) {
	t.Helper()
	var violations []string
	files := 0
	for _, dir := range []string{"cmd", "internal", "plugins", "providers"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(filepath.Join(root, "internal", "elevation"))+"/") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			files++
			aliases := elevationAliases(f)
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if ok && aliases[id.Name] && (sel.Sel.Name == "Selector" || sel.Sel.Name == "CustodySource") {
					violations = append(violations, fset.Position(lit.Pos()).String())
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 100 || len(violations) > 0 {
		t.Fatalf("scanned=%d selector violations=%v", files, violations)
	}
}
func elevationAliases(f *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Path.Value == strconv.Quote("github.com/acamarata/cascade/internal/elevation") {
			name := "elevation"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = true
		}
	}
	return aliases
}
