// Package repo (this file) is the single repository ingest admission guard.
//
// Purpose: decide, for every path a caller wants to index from a git
// working tree, whether its bytes may enter the index at all. Every ingest
// route asks this guard, so no index path bypasses it.
// Inputs: a repository root and repo-relative paths (slash form). Outputs:
// one Admission per path, in input order. An admitted path carries the exact
// bytes the secret detector scanned in Admission.Content, so the caller
// chunks what was scanned and never re-reads the file. A refused
// path carries a Reason and, for a detector hit, the credential class; it
// never carries content, a matched value or an offset into the content.
//
// Constraints: fail closed. Per path: (1) one `git check-ignore --no-index`
// per batch (a git failure refuses every path), (2) ExcludedPath, (3) a
// key-material extension, (4) lstat: a regular, non-symlink file whose
// parent directories contain no symlink, (5) open with O_NONBLOCK (a FIFO
// swapped in cannot hang the ingest; no O_NOFOLLOW, which windows lacks),
// then fstat: regular and os.SameFile with the lstat result, so a symlink
// or file swapped in after lstat is refused unread, (6) fstat size over
// MaxAdmitBytes refuses without a read, (7) a bounded read, (8) Scan, never
// ScanCertain: an entropy-only hit refuses too. A detector or read error
// refuses. The detector is secrets.ProcessDetector; no pattern table here.
// The only AdmitBatch error is cancellation or an oversized batch.
// SPORT: repo/ingest-admission-guard/ADD (contract:repo-ingest-admission).
package repo

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MaxAdmitBytes bounds one admitted file; a larger one is refused unread.
const MaxAdmitBytes = 1 << 20

// MaxAdmitBatch bounds one AdmitBatch call: callers iterate in slices.
const MaxAdmitBatch = 512

// Refusal reasons. Admission.Reason is "" exactly when the path is admitted.
const (
	ReasonIgnored     = "vcs-ignored"
	ReasonExcluded    = "excluded-path"
	ReasonKeyMaterial = "key-material"
	ReasonTooLarge    = "too-large"
	ReasonSecret      = "secret-detected"
	ReasonUnreadable  = "unreadable"
)

// Admission is the guard's verdict on one path.
type Admission struct {
	Rel      string
	Admitted bool
	Reason   string
	// PatternClass: the first hit's credential class on ReasonSecret, never a value.
	PatternClass string
	// Content: the exact bytes scanned, only when Admitted; never logged or
	// serialized, and dropped by the caller after use.
	Content []byte `json:"-"`
}

// syscallNonblock is O_NONBLOCK (defined on every OS; windows ignores it).
const syscallNonblock = syscall.O_NONBLOCK

// Test seams: beforeOpen runs between lstat and open.
var (
	beforeOpen      = func(string) {}
	processDetector = secrets.ProcessDetector
)

// keyMaterialExts hold key material.
var keyMaterialExts = map[string]bool{".pem": true, ".key": true, ".p12": true, ".pfx": true}

// vendoredSegments hold third-party or build output.
var vendoredSegments = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true,
}

// ExcludedPath reports whether rel is never ingestable: empty, absolute, not
// a clean slash path once the OS separator is folded to "/" (NUL, any ":" for
// git pathspec magic and windows streams, "//", a trailing separator, "." or
// ".." parts), a segment starting with "." (.git, .env, .ssh at any depth) or ending in a
// space or dot (windows ignores both), or a vendored or build directory.
func ExcludedPath(rel string) bool {
	p := filepath.ToSlash(rel)
	if p == "" || strings.ContainsRune(p, 0) || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" ||
		p[0] == '/' || strings.ContainsRune(p, ':') || path.Clean(p) != p {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") || strings.TrimRight(seg, " .") != seg || vendoredSegments[seg] {
			return true
		}
	}
	return false
}

// Admit is AdmitBatch of one path.
func Admit(ctx context.Context, repoRoot, rel string) (Admission, error) {
	out, err := AdmitBatch(ctx, repoRoot, []string{rel})
	if err != nil {
		return Admission{Rel: rel}, err
	}
	return out[0], nil
}

// AdmitBatch admits or refuses each of rels, in order. More than
// MaxAdmitBatch paths is a KindInvalidInput error; cancellation is
// KindCanceled. Every other failure refuses the affected path(s).
func AdmitBatch(ctx context.Context, repoRoot string, rels []string) ([]Admission, error) {
	if len(rels) > MaxAdmitBatch {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"repo: ingest batch of %d paths exceeds the %d-path bound", len(rels), MaxAdmitBatch)
	}
	out := make([]Admission, len(rels))
	for i, rel := range rels {
		out[i].Rel = rel
	}
	g := guard{root: repoRoot, rootReal: realDir(repoRoot)}
	ignored, gitErr := gitIgnored(ctx, repoRoot, g.checkable(rels))
	if gitErr != nil {
		if err := canceled(ctx); err != nil {
			return nil, err
		}
		for i := range out {
			out[i].Reason = ReasonUnreadable
		}
		return out, nil
	}
	g.det, g.detErr = processDetector()
	for i := range out {
		if err := canceled(ctx); err != nil {
			return nil, err
		}
		if ignored[filepath.ToSlash(out[i].Rel)] {
			out[i].Reason = ReasonIgnored
			continue
		}
		g.admit(&out[i])
	}
	return out, nil
}

// gitIgnored runs one `git check-ignore -z --no-index --stdin` (--no-index:
// a glob-named path matching a tracked file is still judged) and returns the
// ignored set. Exit 1 = none; any other failure is an error. Callers pass
// only checkable paths: git fails the whole batch on ".." or a symlink.
func gitIgnored(ctx context.Context, repoRoot string, rels []string) (map[string]bool, error) {
	in := &bytes.Buffer{}
	for _, rel := range rels {
		in.WriteString(filepath.ToSlash(rel) + "\x00")
	}
	if in.Len() == 0 {
		return map[string]bool{}, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "check-ignore", "-z", "--no-index", "--stdin")
	cmd.Stdin = in
	stdout := &bytes.Buffer{}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: git check-ignore failed")
		}
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(stdout.String(), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored, nil
}

// canceled returns the typed cancellation error when ctx is done.
func canceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "repo: ingest admission canceled")
	}
	return nil
}

// realDir resolves dir through symlinks, or returns "" when it cannot.
func realDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	if err != nil {
		return ""
	}
	return abs
}

// guard is the per-batch state.
type guard struct {
	root, rootReal string
	det            *secrets.Detector
	detErr         error
}

// admit runs checks (2) to (8) on one path.
func (g *guard) admit(a *Admission) {
	switch {
	case ExcludedPath(a.Rel):
		a.Reason = ReasonExcluded
		return
	case keyMaterialExts[strings.ToLower(filepath.Ext(a.Rel))]:
		a.Reason = ReasonKeyMaterial
		return
	}
	if !g.confined(a.Rel) {
		a.Reason = ReasonExcluded
		return
	}
	full := filepath.Join(g.root, filepath.FromSlash(a.Rel))
	data, reason := g.readBounded(full)
	if reason != "" {
		a.Reason = reason
		return
	}
	if g.detErr != nil || g.det == nil {
		a.Reason = ReasonUnreadable
		return
	}
	if hits := g.det.Scan(data); len(hits) > 0 {
		a.Reason, a.PatternClass = ReasonSecret, string(hits[0].Class)
		return
	}
	a.Admitted, a.Content = true, data
}

// checkable returns the paths git check-ignore can be asked about: not an
// excluded path and not reached through a symlinked directory.
func (g *guard) checkable(rels []string) []string {
	var out []string
	for _, rel := range rels {
		if !ExcludedPath(rel) && g.confined(rel) {
			out = append(out, rel)
		}
	}
	return out
}

// confined reports whether rel's directory resolves, with no symlink on the
// way, to the same place inside the repository, so a symlinked parent
// directory cannot lead the read out of the tree (or into another path).
func (g *guard) confined(rel string) bool {
	if g.rootReal == "" {
		return false
	}
	parent := filepath.Dir(filepath.FromSlash(rel))
	dir := realDir(filepath.Join(g.root, parent))
	return dir != "" && dir == filepath.Join(g.rootReal, parent)
}

// readBounded lstats, opens, fstats and reads full under the rules in the
// file comment. It returns the bytes, or "" bytes and the refusal reason.
func (g *guard) readBounded(full string) ([]byte, string) {
	linfo, err := os.Lstat(full)
	if err != nil {
		return nil, ReasonUnreadable
	}
	if !linfo.Mode().IsRegular() {
		return nil, ReasonExcluded
	}
	beforeOpen(full)
	f, err := os.OpenFile(full, os.O_RDONLY|syscallNonblock, 0)
	if err != nil {
		return nil, ReasonUnreadable
	}
	defer func() { _ = f.Close() }()
	finfo, err := f.Stat()
	if err != nil {
		return nil, ReasonUnreadable
	}
	if !finfo.Mode().IsRegular() || !os.SameFile(linfo, finfo) {
		return nil, ReasonExcluded
	}
	if finfo.Size() > MaxAdmitBytes {
		return nil, ReasonTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxAdmitBytes+1))
	switch {
	case err != nil:
		return nil, ReasonUnreadable
	case len(data) > MaxAdmitBytes:
		return nil, ReasonTooLarge
	}
	return data, ""
}
