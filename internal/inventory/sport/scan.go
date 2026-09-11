// Purpose: walk a given file list and parse every "// SPORT:" line found,
//
//	the shared primitive both BuildRegistry (the real tree, via git
//	ls-files) and internal/build's seeded-violation gate test (an explicit
//	fixture file list) call.
//
// Inputs: repoRoot and a list of repo-relative file paths.
// Outputs: every ParsedEntity found, tagged with its file/line, plus every
//
//	ParseError encountered — never a silent skip of either.
//
// Constraints: a missing file (concurrent-agent working-tree churn) is
//
//	skipped, matching internal/inventory/tree.go's countSPORTLines
//	precedent; a file that exists but fails to read is a hard error.
//
// SPORT: internal.inventory.sport.ScanFiles/ADDED.

package sport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SiteEntity pairs a ParsedEntity with the file/line it came from, before
// Registry deduplication groups it under its Name.
type SiteEntity struct {
	entity ParsedEntity
	file   string
	line   int
}

// ScanFiles parses every "// SPORT:" line in the given repo-relative
// files, read from repoRoot. Multi-line comment continuations (a marker's
// detail text wrapping onto a following "//" line without repeating
// "SPORT:") are not part of the marker text this scans — only the line
// literally containing the marker.
func ScanFiles(repoRoot string, files []string) ([]SiteEntity, []ParseError, error) {
	var entities []SiteEntity
	var errs []ParseError
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("sport: read %s: %w", rel, err)
		}
		fe, fErrs := scanFileContent(rel, data)
		entities = append(entities, fe...)
		errs = append(errs, fErrs...)
	}
	return entities, errs, nil
}

// scanFileContent parses every SPORT marker line in one file's content.
func scanFileContent(rel string, data []byte) ([]SiteEntity, []ParseError) {
	var entities []SiteEntity
	var errs []ParseError
	for i, line := range strings.Split(string(data), "\n") {
		text, ok := markerText(line)
		if !ok {
			continue
		}
		lineNo := i + 1
		parsed, perrs := ParseMarkerLine(rel, lineNo, text)
		for _, p := range parsed {
			entities = append(entities, SiteEntity{entity: p, file: rel, line: lineNo})
		}
		errs = append(errs, perrs...)
	}
	return entities, errs
}

// markerText returns the text after "SPORT:" on line, and whether line
// carries the marker at all — matching internal/inventory/tree.go's
// countSPORTLines trimming convention (leading whitespace, then "//",
// then leading whitespace again, then the literal marker).
func markerText(line string) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
	trimmed = strings.TrimSpace(trimmed)
	if !strings.HasPrefix(trimmed, "SPORT:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, "SPORT:")), true
}
