package parity

import (
	"sort"
	"testing"
	"testing/fstest"
)

func TestShimRefsFindsWholeWords(t *testing.T) {
	fsys := fstest.MapFS{
		"a.go": &fstest.MapFile{
			Data: []byte("var x = capp-shim\nvar y = capp-shimmer\n"),
		},
	}
	refs, err := ShimRefs(fsys, []string{"capp-shim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d: %v", len(refs), refs)
	}
	if refs[0].Path != "a.go" || refs[0].Line != 1 || refs[0].Name != "capp-shim" {
		t.Errorf("unexpected ref: %+v", refs[0])
	}
}

func TestShimRefsSkipsOtherTypes(t *testing.T) {
	fsys := fstest.MapFS{
		"a.png": &fstest.MapFile{
			Data: []byte("capp-shim"),
		},
	}
	refs, err := ShimRefs(fsys, []string{"capp-shim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected 0 refs, got %d: %v", len(refs), refs)
	}
}

func TestShimRefsSorted(t *testing.T) {
	fsys := fstest.MapFS{
		"b.md": &fstest.MapFile{
			Data: []byte("capp-shim\n"),
		},
		"a.yaml": &fstest.MapFile{
			Data: []byte("capp-shim\n"),
		},
		"a.go": &fstest.MapFile{
			Data: []byte("line1\nline2 capp-shim\n"),
		},
	}
	refs, err := ShimRefs(fsys, []string{"capp-shim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d: %v", len(refs), refs)
	}
	if !sort.SliceIsSorted(refs, func(i, j int) bool {
		if refs[i].Path != refs[j].Path {
			return refs[i].Path < refs[j].Path
		}
		return refs[i].Line < refs[j].Line
	}) {
		t.Errorf("refs not sorted: %v", refs)
	}
}
