package recallinject

import (
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

func TestTagUsesInjectedClassifier(t *testing.T) {
	classifier := func(path string) string {
		return "doc"
	}
	r, err := NewRecall(classifier)
	if err != nil {
		t.Fatalf("NewRecall returned error: %v", err)
	}
	got := r.Tag([]string{"a.md"})
	want := map[string]string{"a.md": "doc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tag() = %v, want %v", got, want)
	}
}

func TestNewRecallNilClassifierFails(t *testing.T) {
	_, err := NewRecall(nil)
	if err != ErrNoClassifier {
		t.Errorf("NewRecall(nil) error = %v, want %v", err, ErrNoClassifier)
	}
}

func TestNoArchiveImport(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "recall.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("Failed to parse recall.go: %v", err)
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, "archive") {
			t.Errorf("recall.go imports %q, which contains 'archive'", path)
		}
	}
}
