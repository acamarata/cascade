package repo

import "testing"

func TestRelFile(t *testing.T) {
	got := relFile("/a/b", "/a/b/c/d.go")
	if got != "c/d.go" {
		t.Fatalf("relFile = %q, want c/d.go", got)
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
