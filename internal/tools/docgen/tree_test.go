package docgen

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

func pg(path, body string) Page { return Page{Path: path, Body: []byte(OwnedMarker + "\n" + body)} }

func put(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiffTree(t *testing.T) {
	pages := []Page{pg("a.md", "a"), pg("b.md", "b"), pg("c.md", "c")}
	dir := t.TempDir()
	got, err := DiffTree(dir, pages)
	if err != nil {
		t.Fatal(err)
	}
	want := []Drift{{"a.md", DriftMissing}, {"b.md", DriftMissing}, {"c.md", DriftMissing}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty dir: %v", got)
	}

	put(t, dir, "a.md", string(pages[0].Body))
	put(t, dir, "b.md", OwnedMarker+"\nB")
	put(t, dir, "old.md", OwnedMarker+"\nstale")
	put(t, dir, "notes.md", "hand written")
	got, err = DiffTree(dir, pages)
	if err != nil {
		t.Fatal(err)
	}
	want = []Drift{{"b.md", DriftChanged}, {"c.md", DriftMissing}, {"old.md", DriftExtra}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed: %v", got)
	}

	old := readFile
	defer func() { readFile = old }()
	boom := errors.New("boom")
	readFile = func(string) ([]byte, error) { return nil, boom }
	got, err = DiffTree(dir, pages)
	if err == nil || got != nil || !errors.Is(err, boom) || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("read error: %v %v", got, err)
	}
}

func TestDiffTreeBadInput(t *testing.T) {
	for _, ps := range [][]Page{{{Path: "../x.md"}}, {{Path: ""}}, {{Path: "a.md"}, {Path: "a.md"}}, {{Path: "/abs.md"}}, {{Path: "C:/x.md"}}, {{Path: `\\srv\x.md`}}} {
		if _, err := DiffTree(t.TempDir(), ps); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%v: %v", ps, err)
		}
		if err := WriteTree(t.TempDir(), ps); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("write %v: %v", ps, err)
		}
	}
}

func TestWriteTree(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "gone.md", OwnedMarker+"\nold")
	put(t, dir, "keep.txt", "mine")
	put(t, dir, "other.md", "hand written")
	pages := []Page{pg("a.md", "a"), pg("sub/b.md", "b")}
	if err := WriteTree(dir, pages); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.md")); !os.IsNotExist(err) {
		t.Fatalf("extra owned page kept: %v", err)
	}
	for _, f := range []string{"keep.txt", "other.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("non-owned %s removed", f)
		}
	}
	if d, err := DiffTree(dir, pages); err != nil || len(d) != 0 {
		t.Fatalf("after write: %v %v", d, err)
	}

	dir2 := t.TempDir()
	put(t, dir2, "a.md", "hand written")
	put(t, dir2, "gone.md", OwnedMarker+"\nold")
	err := WriteTree(dir2, []Page{pg("z.md", "z"), pg("a.md", "a")})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir2, "a.md")); string(b) != "hand written" {
		t.Fatal("conflicting file overwritten")
	}
	for _, f := range []string{"z.md"} {
		if _, err := os.Stat(filepath.Join(dir2, f)); !os.IsNotExist(err) {
			t.Fatalf("%s written despite conflict", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir2, "gone.md")); err != nil {
		t.Fatal("owned page deleted despite conflict")
	}
}

func TestWriteTreeReadError(t *testing.T) {
	old := readFile
	defer func() { readFile = old }()
	readFile = func(string) ([]byte, error) { return nil, errors.New("boom") }
	if err := WriteTree(t.TempDir(), []Page{pg("a.md", "a")}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatal(err)
	}
}

func TestRender(t *testing.T) {
	root := &cobra.Command{Use: "cascade", Short: "root"}
	sub := &cobra.Command{Use: "run", Short: "runs", Long: "long text", Run: func(*cobra.Command, []string) {}}
	sub.Flags().String("name", "", "a name")
	root.AddCommand(sub, &cobra.Command{Use: "hid", Hidden: true, Run: func(*cobra.Command, []string) {}})
	pages := RenderCommands(root)
	if len(pages) != 2 || pages[0].Path != "cascade.md" || pages[1].Path != "cascade_run.md" {
		t.Fatalf("pages: %v", pages)
	}
	if s := string(pages[1].Body); !strings.Contains(s, "--name") || !strings.Contains(s, "long text") {
		t.Fatal(s)
	}
	if !strings.Contains(string(pages[0].Body), "[cascade run](cascade_run.md)") {
		t.Fatal(string(pages[0].Body))
	}
	k := RenderConfigKeys("keys.md", []ConfigKey{{Key: "b", Doc: "x|y"}, {Key: "a", Type: "int"}})
	s := string(k.Body)
	if strings.Index(s, "`a`") > strings.Index(s, "`b`") || !strings.Contains(s, `x\|y`) {
		t.Fatal(s)
	}
}
