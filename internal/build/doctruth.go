// Package build (this file) implements the public-doc truth gate
// (P1-DOC-07, sot/constitution.md#c20): every tracked public doc's links,
// anchors, backticked repo-path/line/symbol/test citations, and prose stay
// true to the tree at HEAD, and a doc carries no stale-forward-claim phrase
// or TODO/FIXME/TBD/XXX/PLACEHOLDER marker outside code.
//
// The gate never edits a doc and never executes doc content (C15 MEDIUM —
// this is a quality gate, not a security seam). CI mode compares findings
// against a committed, prune-only baseline ratchet
// (internal/build/testdata/doctruth-baseline.json) so a ticket can land
// before every existing doc is fixed without a new stale doc ever landing
// clean; release mode ignores the baseline and is the Phase-completion
// check (P1-EXIT-13).
//
// Rule implementations live in doctruth_scope.go (scope + shared parser:
// fenced/inline-code tracker, heading slugs), doctruth_links.go (link,
// anchor), doctruth_refs.go (path, line, symbol, test), doctruth_claims.go
// (claim, directive), doctruth_index.go (index), and doctruth_baseline.go
// (the baseline file and CI/Release comparison). This file holds the
// shared types and the CheckDocTruth orchestrator.
package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DocRule identifies which doctruth rule produced a DocFinding.
type DocRule string

// The nine doctruth rules (P1-DOC-07 outputs contract).
const (
	DocRuleLink      DocRule = "link"
	DocRuleAnchor    DocRule = "anchor"
	DocRulePath      DocRule = "path"
	DocRuleLine      DocRule = "line"
	DocRuleSymbol    DocRule = "symbol"
	DocRuleTest      DocRule = "test"
	DocRuleClaim     DocRule = "claim"
	DocRuleIndex     DocRule = "index"
	DocRuleDirective DocRule = "directive"
)

// DocFinding is one doctruth violation. Key is stable when lines move
// elsewhere in the file, unique per finding (a second identical offending
// line in the same file gets a new occurrence index), and File is always
// slash-separated regardless of GOOS.
type DocFinding struct {
	File   string
	Line   int
	Rule   DocRule
	Detail string
	Key    string
}

// DocTruthMode selects the baseline behavior: DocTruthCI passes a
// baselined finding, DocTruthRelease fails on every finding. Any value
// other than DocTruthCI is treated as DocTruthRelease (the strictest).
type DocTruthMode int

// The two doctruth modes.
const (
	DocTruthCI DocTruthMode = iota
	DocTruthRelease
)

// DocTruthReport summarizes one CheckDocTruth run. New holds findings not
// in the baseline (CI) or every finding (Release). Fixed holds baseline
// keys that no longer reproduce anywhere in the scanned scope.
type DocTruthReport struct {
	Files      int
	Links      int
	Refs       int
	Directives int
	Findings   []DocFinding
	New        []DocFinding
	Fixed      []string
}

// Injection seams: only _test.go files in this package reassign these.
var (
	gitLsFilesFn  = realGitLsFiles
	gitShowFn     = realGitShow
	readFileFn    = os.ReadFile
	parseGoFileFn = realParseGoFile
)

// realParseGoFile is the production go/parser entry point; tests that
// replace parseGoFileFn to inject a failure for one file delegate every
// other file back to this.
func realParseGoFile(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
	return parser.ParseFile(fset, filename, src, 0)
}

func realGitLsFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("doctruth: git ls-files: %w", err)
	}
	trimmed := strings.TrimRight(string(out), "\x00")
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(trimmed, "\x00")
	for i, p := range parts {
		parts[i] = filepath.ToSlash(p)
	}
	return parts, nil
}

func realGitShow(repoRoot, ref, path string) ([]byte, error) {
	out, err := exec.Command("git", "-C", repoRoot, "show", ref+":"+path).Output()
	if err != nil {
		return nil, fmt.Errorf("doctruth: git show %s:%s: %w", ref, path, err)
	}
	return out, nil
}

// docTruthScopePrefixes are the tracked path prefixes doctruth scans.
var docTruthScopePrefixes = []string{"docs/", ".github/"}

// docTruthExcludedPrefixes hold dated records: never stale-checked.
var docTruthExcludedPrefixes = []string{
	"docs/adrs/", "docs/releases/", "docs/spikes/", "docs/waves/",
}

// DocTruthScope returns the tracked public-doc universe: README.md,
// docs/**/*.md, .github/**/*.md, plugins/*/README.md, minus the dated
// record directories and CHANGELOG.md. Slash-separated, sorted.
func DocTruthScope(repoRoot string) ([]string, error) {
	all, err := gitLsFilesFn(repoRoot)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "doctruth: listing tracked files")
	}
	var out []string
	for _, f := range all {
		if !strings.HasSuffix(f, ".md") {
			continue
		}
		if !inDocTruthScope(f) {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

func inDocTruthScope(f string) bool {
	if f == "CHANGELOG.md" {
		return false
	}
	for _, p := range docTruthExcludedPrefixes {
		if strings.HasPrefix(f, p) {
			return false
		}
	}
	if f == "README.md" {
		return true
	}
	for _, p := range docTruthScopePrefixes {
		if strings.HasPrefix(f, p) {
			return true
		}
	}
	if strings.HasPrefix(f, "plugins/") {
		rest := strings.TrimPrefix(f, "plugins/")
		segs := strings.Split(rest, "/")
		if len(segs) == 2 && segs[1] == "README.md" {
			return true
		}
	}
	return false
}

// keyer generates stable, unique DocFinding keys: File + "|" + Rule + "|"
// + first 12 hex of sha256(trimmed line text) + "|" + a 1-based occurrence
// index counted per (File, Rule, line text) seen so far.
type keyer struct{ counts map[string]int }

func newKeyer() *keyer { return &keyer{counts: map[string]int{}} }

// CheckDocTruth scans files (or the whole DocTruthScope when files is
// empty) and returns a DocTruthReport. See the package doc comment for the
// mode semantics.
func CheckDocTruth(repoRoot string, mode DocTruthMode, files ...string) (DocTruthReport, error) {
	rep, findings, err := computeFindings(repoRoot, files...)
	if err != nil {
		return DocTruthReport{}, err
	}
	baseline, err := loadBaseline(repoRoot)
	if err != nil {
		return DocTruthReport{}, err
	}
	inFileScope := func(file string) bool {
		if len(rep.scannedFiles) == 0 {
			return true
		}
		return rep.scannedFiles[file]
	}
	rep.report.New, rep.report.Fixed = applyBaseline(findings, baseline, mode, inFileScope)
	rep.report.Findings = findings
	return rep.report, nil
}

// wrapReadErr classifies a file-read failure into the taxonomy: a missing
// file is KindNotFound, anything else (permission, I/O) is KindInternal.
func wrapReadErr(msg, path string, err error) error {
	if os.IsNotExist(err) {
		return cascade.Wrapf(cascade.KindNotFound, err, "%s: %s", msg, path)
	}
	return cascade.Wrapf(cascade.KindInternal, err, "%s: %s", msg, path)
}

func (k *keyer) key(file string, rule DocRule, lineText string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(lineText)))
	hash := hex.EncodeToString(sum[:])[:12]
	base := file + "|" + string(rule) + "|" + hash
	k.counts[base]++
	return fmt.Sprintf("%s|%d", base, k.counts[base])
}
