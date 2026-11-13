package parity

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Ref represents a single whole-word reference to a shim name in a file.
type Ref struct {
	// Path is the slash-separated path of the file within the fs.FS.
	Path string
	// Line is the 1-based line number where the reference was found.
	Line int
	// Name is the shim name that matched.
	Name string
}

// ShimRefs walks fsys for .go, .md and .yaml files and returns a Ref for each
// whole-word occurrence of any name in names. Results are sorted by path then
// line. It never reads outside fsys.
func ShimRefs(fsys fs.FS, names []string) ([]Ref, error) {
	var refs []Ref
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".md" && ext != ".yaml" {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			for _, name := range names {
				if containsWholeWord(line, name) {
					refs = append(refs, Ref{
						Path: path,
						Line: i + 1,
						Name: name,
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Path != refs[j].Path {
			return refs[i].Path < refs[j].Path
		}
		return refs[i].Line < refs[j].Line
	})
	return refs, nil
}

// containsWholeWord reports whether text contains target as a whole word.
// Word boundaries are defined by Unicode letters and digits.
func containsWholeWord(text, target string) bool {
	idx := 0
	for {
		pos := strings.Index(text[idx:], target)
		if pos < 0 {
			return false
		}
		absPos := idx + pos
		before := absPos - 1
		after := absPos + len(target)
		okBefore := before < 0 || !isWordChar(rune(text[before]))
		okAfter := after >= len(text) || !isWordChar(rune(text[after]))
		if okBefore && okAfter {
			return true
		}
		idx = absPos + 1
	}
}

// isWordChar reports whether r is a letter or digit.
func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}
