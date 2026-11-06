package build

import (
	"bytes"
	"errors"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func rpcFixture(t *testing.T, name string) string {
	t.Helper()
	return materializeFixtureGitRepo(t, filepath.Join("testdata", "seeded-violations", "rpcindex", name))
}

// TestRPCMethodReference_SeededRed proves the exact fixture finding set.
func TestRPCMethodReference_SeededRed(t *testing.T) {
	root := rpcFixture(t, "violations")
	rows, findings, err := BuildRPCMethodRows(root)
	if err != nil || len(rows) != 6 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	want := []string{
		"rpc_access|fixture.noaccess", "rpc_access|cmd/cascade/verbs.go:9",
		"rpc_access|cmd/cascade/verbs.go:11", "rpc_elevation|cmd/cascade/verbs.go:12",
		"rpc_doc|fixture.undocumented", "rpc_unregistered|fixture.ghost",
		"rpc_access|fixture.ghost", "rpc_access|fixture.conflict",
	}
	got := map[string]bool{}
	for _, f := range findings {
		got[f.Key] = true
		if !strings.HasSuffix(f.Detail, ": "+strings.SplitN(f.Key, "|", 2)[1]) {
			t.Errorf("bad detail: %+v", f)
		}
	}
	if len(findings) != len(want) {
		t.Fatalf("findings=%+v", findings)
	}
	for _, key := range want {
		if !got[key] {
			t.Errorf("missing %s: %+v", key, findings)
		}
	}
	for _, row := range rows {
		if (row.Method == "fixture.variable" || row.Method == "fixture.conflict") && row.Access != "" {
			t.Errorf("unresolved access became %q for %s", row.Access, row.Method)
		}
		if row.Elevation != "" {
			t.Errorf("unresolved elevation became %q for %s", row.Elevation, row.Method)
		}
	}
}

// TestRPCMethodReference_CleanFixtureRenders proves rows and stable rendering.
func TestRPCMethodReference_CleanFixtureRenders(t *testing.T) {
	root := rpcFixture(t, "clean")
	rows, findings, err := BuildRPCMethodRows(root)
	want := []RPCMethodRow{
		{"fixture.bare", "Open", "none", "docs/rpc-protocol.md", "fixturebare"},
		{"fixture.conditional", "Scoped", "conditional", "docs/reference/rpc/verbs.md", "verbs"},
		{"fixture.qualified", "OwnerView", "always", "docs/rpc-protocol.md", "fixturequalified"},
	}
	if err != nil || len(findings) != 0 || !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows=%+v findings=%+v err=%v", rows, findings, err)
	}
	first := RenderRPCMethodReference(rows)
	if len(first) == 0 || !bytes.Equal(first, RenderRPCMethodReference(rows)) || bytes.Contains(first, []byte("\r")) {
		t.Fatal("render is empty or unstable")
	}
	for _, part := range []string{"# RPC method reference\n\n", "| Method | Access | Elevation | Documented in |", "../rpc-protocol.md#fixturebare", "rpc/verbs.md#verbs"} {
		if !strings.Contains(string(first), part) {
			t.Errorf("missing %q in %s", part, first)
		}
	}
}

// TestRPCMethodReference_ElevationRuleLiterals rejects ambiguous rule literals.
func TestRPCMethodReference_ElevationRuleLiterals(t *testing.T) {
	root := rpcFixture(t, "elevation")
	got, unresolved, err := ScanElevatedVerbs(root)
	want := map[string]string{"fixture.always": "always", "fixture.conditional": "conditional", "fixture.both": "", "fixture.neither": "", "fixture.flag": ""}
	if err != nil || !reflect.DeepEqual(got, want) || len(unresolved) != 3 {
		t.Fatalf("got=%v unresolved=%v err=%v", got, unresolved, err)
	}
	for i, line := range []string{"verbs.go:8:", "verbs.go:9:", "verbs.go:10:"} {
		if !strings.HasPrefix(unresolved[i], line) {
			t.Errorf("unresolved=%v", unresolved)
		}
	}
}

// TestRPCMethodReference_FailsClosed proves input failures never return rows.
func TestRPCMethodReference_FailsClosed(t *testing.T) {
	t.Run("missing golden", func(t *testing.T) {
		_, err := LoadRPCMethodGolden(t.TempDir())
		rpcError(t, err, cascade.KindUnavailable, "rpcindex: reading method golden")
	})
	t.Run("comment golden", func(t *testing.T) {
		root := rpcFixture(t, "clean")
		withReadFile(t, func(p string) ([]byte, error) {
			if strings.HasSuffix(p, ".golden") {
				return []byte("  # only comments\n\n"), nil
			}
			return os.ReadFile(p)
		})
		_, err := LoadRPCMethodGolden(root)
		rpcError(t, err, cascade.KindInvalidInput, "rpcindex: method golden contains no methods")
	})
	for _, mode := range []string{"read", "parse", "syntax", "git", "doc"} {
		t.Run(mode, func(t *testing.T) { rpcFailureCase(t, mode) })
	}
	rpcFrozenKinds(t)
}

func rpcFailureCase(t *testing.T, mode string) {
	t.Helper()
	root := rpcFixture(t, "clean")
	source := filepath.Join(root, "internal", "rpc", "verbs.go")
	switch mode {
	case "read", "doc":
		target := source
		if mode == "doc" {
			target = filepath.Join(root, "docs", "rpc-protocol.md")
		}
		withReadFile(t, func(p string) ([]byte, error) {
			if p == target {
				return nil, errors.New("fixture read failure")
			}
			return os.ReadFile(p)
		})
	case "parse":
		withParseGoFile(t, func(fs *token.FileSet, p string, b []byte) (*ast.File, error) {
			if p == source {
				return nil, errors.New("fixture parser failure")
			}
			return realParseGoFile(fs, p, b)
		})
	case "syntax":
		if err := os.WriteFile(source, []byte("package rpc\nfunc broken("), 0o644); err != nil {
			t.Fatal(err)
		}
	case "git":
		withGitLsFiles(t, func(string) ([]string, error) { return nil, errors.New("fixture git failure") })
	}
	rows, findings, err := BuildRPCMethodRows(root)
	if err == nil || rows != nil || findings != nil {
		t.Fatalf("rows=%v findings=%v err=%v", rows, findings, err)
	}
	if mode != "git" && mode != "doc" && !strings.Contains(err.Error(), "internal/rpc/verbs.go") {
		t.Fatalf("error lacks source: %v", err)
	}
	rpcFailureError(t, err, mode)
}

func rpcFailureError(t *testing.T, err error, mode string) {
	t.Helper()
	kind, message := cascade.KindUnavailable, "rpcindex: reading internal/rpc/verbs.go"
	switch mode {
	case "parse", "syntax":
		kind, message = cascade.KindInvalidInput, "rpcindex: parsing internal/rpc/verbs.go"
	case "git":
		message = "rpcindex: listing tracked files"
	case "doc":
		message = "rpcindex: reading docs/rpc-protocol.md"
	}
	rpcError(t, err, kind, message)
}

func rpcError(t *testing.T, err error, kind cascade.Kind, message string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != kind || ce.Msg != message {
		t.Fatalf("error=%v want %s: %s", err, kind, message)
	}
}

func rpcFrozenKinds(t *testing.T) {
	t.Helper()
	kinds := []cascade.Kind{cascade.KindNotFound, cascade.KindInvalidInput, cascade.KindConflict, cascade.KindUnavailable,
		cascade.KindTimeout, cascade.KindCanceled, cascade.KindPermissionDenied, cascade.KindElevationRequired,
		cascade.KindPolicyDenied, cascade.KindCapabilityDenied, cascade.KindQuotaExhausted, cascade.KindUnsupported,
		cascade.KindIntegrity, cascade.KindInternal}
	if !reflect.DeepEqual(cascade.AllKinds(), kinds) {
		t.Fatal("error taxonomy changed")
	}
	for _, kind := range kinds {
		t.Run(kind.String(), func(t *testing.T) {
			withReadFile(t, func(string) ([]byte, error) { return nil, cascade.New(kind, "fixture error") })
			_, err := LoadRPCMethodGolden(t.TempDir())
			rpcError(t, err, cascade.KindUnavailable, "rpcindex: reading method golden")
			rpcError(t, errors.Unwrap(err), kind, "fixture error")
		})
	}
}
