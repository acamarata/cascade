package acceptance

// Purpose (this file): the SEEDED journal tree the default-lane parity tests
//   mutate. testdata/widget_parity/seed holds one complete, passing journal
//   with its evidence and a stand-in artifact; every test copies it into a
//   fresh temp directory, applies ONE defect and asserts the verifier's
//   answer. The copy has a sibling slot so a path that escapes the journal
//   directory lands on a real file, not on a missing one.
// Constraints: edits assert their target exists, so a mutation that matches
//   nothing fails the test instead of passing vacuously.
// Provenance of the fixtures: testdata/README.md.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	seedRCTag    = "rc-seeded-1"
	seedSrc      = "testdata/widget_parity/seed"
	seedArtifact = "artifact/CascadeMenuBar.dmg"
)

// seedTree is one mutable copy of the seeded journal.
type seedTree struct {
	t    *testing.T
	root string // holds dir and any file a defect plants outside it
	dir  string // the journal directory
}

// newSeedTree copies the seeded tree into a fresh temp directory.
func newSeedTree(t *testing.T) *seedTree {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "tree")
	err := filepath.WalkDir(seedSrc, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(seedSrc, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o600)
	})
	if err != nil {
		t.Fatalf("copy seeded tree: %v", err)
	}
	return &seedTree{t: t, root: root, dir: dir}
}

func (s *seedTree) path(rel string) string {
	return filepath.Join(s.dir, filepath.FromSlash(rel))
}

// seededProbe is the seeded stand-in for codesign and spctl: it returns the
// recorded probe that sits next to the seeded artifact.
func seededProbe(suffix string) func(string) (string, error) {
	return func(artifact string) (string, error) {
		b, err := os.ReadFile(artifact + suffix)
		return string(b), err
	}
}

func (s *seedTree) env() parityEnv {
	return parityEnv{rcTag: seedRCTag, artifact: s.path(seedArtifact), codesignOf: seededProbe(".codesign-probe"), spctlOf: seededProbe(".spctl-probe")}
}

func (s *seedTree) verify() parityResult {
	return verifyParityJournal(s.path("journal.md"), s.env())
}

// write replaces (or plants) a file; rel may start with ../ to land in root.
func (s *seedTree) write(rel, content string) {
	s.t.Helper()
	p := s.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *seedTree) read(rel string) string {
	s.t.Helper()
	b, err := os.ReadFile(s.path(rel))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func (s *seedTree) remove(rel string) {
	s.t.Helper()
	if err := os.Remove(s.path(rel)); err != nil {
		s.t.Fatalf("seeded file to remove: %v", err)
	}
}

// replaceIn swaps old for new in a file and fails when old is absent.
func (s *seedTree) replaceIn(rel, old, repl string) {
	s.t.Helper()
	text := s.read(rel)
	if !strings.Contains(text, old) {
		s.t.Fatalf("%s does not contain %q: the mutation would be vacuous", rel, old)
	}
	s.write(rel, strings.Replace(text, old, repl, 1))
}

// blankEvidence empties every evidence file in the tree.
func (s *seedTree) blankEvidence() {
	s.t.Helper()
	err := filepath.WalkDir(s.path("evidence"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.WriteFile(p, nil, 0o600)
	})
	if err != nil {
		s.t.Fatal(err)
	}
}

// rowLine finds a locator's journal line index.
func (s *seedTree) rowLine(lines []string, loc string) int {
	s.t.Helper()
	for i, l := range lines {
		if strings.HasPrefix(l, "| "+parityRowPrefix+loc+" |") {
			return i
		}
	}
	s.t.Fatalf("seeded journal has no row for %s", loc)
	return -1
}

// editJournal rewrites the journal's lines through f.
func (s *seedTree) editJournal(f func(lines []string) []string) {
	s.t.Helper()
	lines := strings.Split(strings.TrimRight(s.read("journal.md"), "\n"), "\n")
	s.write("journal.md", strings.Join(f(lines), "\n")+"\n")
}

// setCell sets one cell (1 status, 2 evidence, 3 rc tag, 4 sha) of a row.
func (s *seedTree) setCell(loc string, col int, v string) {
	s.t.Helper()
	s.editJournal(func(lines []string) []string {
		i := s.rowLine(lines, loc)
		c := parityCells(lines[i])
		c[col] = v
		lines[i] = "| " + strings.Join(c, " | ") + " |"
		return lines
	})
}

func (s *seedTree) dropRow(loc string) {
	s.t.Helper()
	s.editJournal(func(lines []string) []string {
		i := s.rowLine(lines, loc)
		return append(lines[:i], lines[i+1:]...)
	})
}

// dupRow appends a copy of a row at the end.
func (s *seedTree) dupRow(loc string) {
	s.t.Helper()
	s.editJournal(func(lines []string) []string {
		return append(lines, lines[s.rowLine(lines, loc)])
	})
}

// addLine appends a raw line to the journal.
func (s *seedTree) addLine(line string) {
	s.t.Helper()
	s.editJournal(func(lines []string) []string { return append(lines, line) })
}

// expectRed fails unless the verifier is red, every problem names loc, and
// one of them says want.
func (s *seedTree) expectRed(loc, want string) {
	s.t.Helper()
	res := s.verify()
	if len(res.problems) == 0 {
		s.t.Fatalf("verifier is green; want a problem about %s containing %q", loc, want)
	}
	found := false
	for _, p := range res.problems {
		if !strings.HasPrefix(p, parityRowPrefix+loc+":") {
			s.t.Errorf("problem does not name %s: %s", loc, p)
		}
		found = found || strings.Contains(p, want)
	}
	if !found {
		s.t.Errorf("no problem contains %q; got %v", want, res.problems)
	}
}
