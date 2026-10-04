//go:build capmap

package capmap

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Repo-relative homes of the two inputs the harness reads.
const (
	tableRelPath = "docs/capability-map.md"
	idsRelPath   = "internal/build/testdata/capability-ids.txt"
)

// block is one run of consecutive pipe lines. start is the 0-based line
// index of its first line.
type block struct {
	start int
	lines []string
}

// tableBlocks returns every run of consecutive lines that begin with "|",
// skipping fenced code so an example in the intro is never read as data.
// A non-blank line holding a pipe that directly follows a run joins it, so
// a row written without its leading pipe (which markdown still renders as a
// row) reaches parseBlock and is reported there instead of being dropped.
func tableBlocks(text string) []block {
	var out []block
	inFence, open := false, false
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence, open = !inFence, false
			continue
		}
		startsRow := strings.HasPrefix(trimmed, "|")
		continues := open && strings.Contains(trimmed, "|")
		if inFence || (!startsRow && !continues) {
			open = false
			continue
		}
		if !open {
			out = append(out, block{start: i})
			open = true
		}
		last := &out[len(out)-1]
		last.lines = append(last.lines, trimmed)
	}
	return out
}

// cells splits one pipe-table line into trimmed cells. A cell cannot
// contain a pipe.
func cells(line string) []string {
	line = strings.TrimPrefix(strings.TrimSpace(line), "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// parseTable reads the one six-column table in text. It returns the rows it
// could read and a finding for every structural defect: no table, a second
// table, a wrong header or separator, a row with the wrong column count, an
// empty cell.
func parseTable(text string) ([]row, []finding) {
	blocks := tableBlocks(text)
	if len(blocks) == 0 {
		return nil, []finding{{Code: findNoTable, Detail: "no pipe table found"}}
	}
	var fs []finding
	for _, b := range blocks[1:] {
		fs = append(fs, finding{Code: findSecondTable, Line: b.start + 1, Detail: "only one table is allowed"})
	}
	rows, more := parseBlock(blocks[0])
	return rows, append(fs, more...)
}

// parseBlock checks the header and separator of b and reads its rows.
func parseBlock(b block) ([]row, []finding) {
	if got := cells(b.lines[0]); !slices.Equal(got, tableHeader) {
		return nil, []finding{{Code: findBadHeader, Line: b.start + 1,
			Detail: "header is [" + strings.Join(got, " | ") + "], want [" + strings.Join(tableHeader, " | ") + "]"}}
	}
	if !isSeparator(b.lines) {
		return nil, []finding{{Code: findBadSeparator, Line: b.start + 2, Detail: "header must be followed by a separator row"}}
	}
	var rows []row
	var fs []finding
	for i, line := range b.lines[2:] {
		lineNo := b.start + 3 + i
		c := cells(line)
		if !strings.HasPrefix(line, "|") || len(c) != len(tableHeader) {
			fs = append(fs, finding{Code: findBadRow, Line: lineNo, Detail: "row must start with a pipe and have the right number of columns"})
			continue
		}
		r := row{line: lineNo, capability: c[0], entrypoint: c[1], productionPath: c[2], class: c[3], owner: c[4], evidence: c[5]}
		for j, cell := range c {
			if cell == "" {
				fs = append(fs, finding{Code: findEmptyCell, Line: lineNo, Capability: r.capability, Detail: "column " + tableHeader[j] + " is empty"})
			}
		}
		rows = append(rows, r)
	}
	return rows, fs
}

// isSeparator reports whether the second line of a block is a six-cell
// markdown separator row.
func isSeparator(lines []string) bool {
	if len(lines) < 2 {
		return false
	}
	c := cells(lines[1])
	if len(c) != len(tableHeader) {
		return false
	}
	for _, cell := range c {
		if !separatorCell.MatchString(cell) {
			return false
		}
	}
	return true
}

// loadInputs reads the table text and the capability id list from explicit
// paths. Blank lines and lines starting with "#" in the id list are
// ignored. Both files must exist.
func loadInputs(tablePath, idsPath string) (table string, ids []string, err error) {
	raw, err := os.ReadFile(tablePath)
	if err != nil {
		return "", nil, cascade.Wrap(cascade.KindInvalidInput, err, "capability map: read table")
	}
	idsRaw, err := os.ReadFile(idsPath)
	if err != nil {
		return "", nil, cascade.Wrap(cascade.KindInvalidInput, err, "capability map: read capability ids")
	}
	for _, line := range strings.Split(string(idsRaw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			ids = append(ids, line)
		}
	}
	return string(raw), ids, nil
}

// TestCapmapTable_Evaluates runs every verified row's registered probe as a
// subtest, records which probes passed, then evaluates the committed table
// with that set. A header-only table fails it, naming every capability
// that has no row.
func TestCapmapTable_Evaluates(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	table, ids, err := loadInputs(filepath.Join(root, filepath.FromSlash(tableRelPath)),
		filepath.Join(root, filepath.FromSlash(idsRelPath)))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := parseTable(table)
	passed := runProbes(t, verifiedProbeNames(rows), probes)
	if err := findingsError(evaluate(table, ids, passed)); err != nil {
		t.Fatal(err)
	}
}
