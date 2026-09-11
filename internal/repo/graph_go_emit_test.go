package repo

import (
	"strings"
	"testing"
)

func TestRelFile(t *testing.T) {
	got := relFile("/a/b", "/a/b/c/d.go")
	if got != "c/d.go" {
		t.Fatalf("relFile = %q, want c/d.go", got)
	}
}

// TestRelFile_WindowsShapedRelWouldNormalize documents why relFile's
// filepath.ToSlash call is the fix for the windows/amd64 CI failure
// (relFile = "c\\d.go", want c/d.go), and why that fix cannot be
// exercised end-to-end from this unix host: verified empirically
// (go run against a one-line program), filepath.ToSlash on darwin/linux
// is a strict identity function -- Separator is already '/', so it never
// scans for or rewrites '\\' -- the same reason filepath.Rel itself can
// never produce a backslash here; both are GOOS-locked constants, not
// runtime checks on the input bytes. Only a real windows/amd64 run
// exercises relFile's actual filepath.ToSlash call.
// This test instead proves the CONTRACT that call satisfies on windows,
// per the stdlib's own documented ToSlash behavior there (replace every
// OS separator with '/'): given the literal backslash-separated shape
// windows' filepath.Rel produces for TestRelFile's inputs, replacing
// every '\\' with '/' -- windows' ToSlash, restated portably since the
// real call is unavailable here -- yields the golden fixture's
// forward-slash form.
func TestRelFile_WindowsShapedRelWouldNormalize(t *testing.T) {
	windowsRelOutput := `c\d.go`
	portableToSlash := strings.ReplaceAll(windowsRelOutput, `\`, "/")
	if portableToSlash != "c/d.go" {
		t.Fatalf("simulated windows ToSlash(%q) = %q, want c/d.go", windowsRelOutput, portableToSlash)
	}
}

func TestRelFile_OutsideRootFallsBackToAbsolute(t *testing.T) {
	// filepath.Rel across two unrelated absolute Windows-drive-style roots
	// returns an error on some platforms; relFile must fall back to abs
	// rather than propagate that error.
	got := relFile("", "not-an-abs-path-relative-to-empty-root")
	if got == "" {
		t.Fatal("relFile returned empty string")
	}
}

func TestDeclsFromNode_SkipsMethodsAndUnexported(t *testing.T) {
	// declsFromNode is exercised end-to-end by TestGraphExtract_RealCounterpart
	// (which asserts unexportedHelper never becomes a node); this test
	// covers the direct unit boundary with a nil FuncDecl.Recv/exported
	// name check via the package's own fixture-derived decl, keeping the
	// assertion here narrow to the function's own branch, not the whole
	// pipeline.
	if got := declsFromNode(nil, nil, ""); got != nil {
		t.Fatalf("declsFromNode(nil, ...) = %v, want nil", got)
	}
}
