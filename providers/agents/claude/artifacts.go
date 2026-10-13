// Purpose: Artifacts: the modified paths of a job's leased tree from `git
//   status --porcelain -z`, run with every config-driven execution path of
//   git disabled or refused (fix item 9), and the canary scan of every
//   modified path's name and content (R-21.152; T0 decision F3).
// Inputs: the job's Lease (root and the lease's own git directory).
// Outputs: tree-relative modified paths, excluding the files the driver
//   itself injected; a non-nil empty slice when none; ErrCredentialCanary
//   when a name or a content holds the canary in any form.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   out for the 300-line cap. git runs with GIT_CONFIG_NOSYSTEM=1,
//   GIT_CONFIG_GLOBAL=/dev/null, GIT_ATTR_NOSYSTEM=1, PATH only, the lease's
//   --git-dir, --no-optional-locks, core.fsmonitor and core.hooksPath
//   neutralized, core.attributesFile=/dev/null and submodules ignored; a
//   repository whose config declares any filter clean/smudge/process
//   driver, or whose attributes reference any filter, is refused before
//   status runs. A child can still change .git/config between that check
//   and the status call: the residual window is recorded, not hidden.
//   Content is read without following links, streamed through a
//   canaryScanner (bounded memory) and bounded in time by ctx; a path that
//   cannot be read, or is not a regular file, directory or symlink, fails
//   the scan closed.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrGitExecDriver refuses a repository that configures a git filter or
// process driver, which `git status` could execute inside the daemon.
var ErrGitExecDriver = cascade.New(cascade.KindCapabilityDenied, "claude: repository declares a git filter/process driver")

var filterAttr = regexp.MustCompile(`(^|\s)[-!]?filter(=|\s|$)`)

// Artifacts returns the job's modified paths.
func (d *Driver) Artifacts(ctx context.Context, id provider.AgentJobID) ([]string, error) {
	rec, err := d.lookup(id)
	if err != nil {
		return nil, err
	}
	return d.artifactsFor(ctx, rec)
}

func (d *Driver) artifactsFor(ctx context.Context, rec *jobRecord) ([]string, error) {
	paths, ignored, err := gitModified(ctx, rec.lease)
	if err != nil {
		return nil, err
	}
	for _, p := range ignored {
		if err := scanIgnored(ctx, rec.lease.Root, p, rec.canary.forms); err != nil {
			return nil, err
		}
	}
	out := []string{}
	for _, p := range paths {
		if containsCanary([]byte(p), rec.canary.forms) {
			return nil, ErrCredentialCanary
		}
		if err := scanArtifact(ctx, rec.lease.Root, p, rec.canary.forms); err != nil {
			return nil, err
		}
		if !rec.injected[p] { // the driver's own files are scanned, never listed
			out = append(out, p)
		}
	}
	return out, nil
}

// gitEnv is the only environment git sees: no system or global config.
func gitEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ATTR_NOSYSTEM=1"}
}

func gitArgs(l Lease, sub ...string) []string {
	return append([]string{"--no-optional-locks", "--git-dir", l.GitDir, "--work-tree", l.Root,
		"-c", "core.fsmonitor=", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.attributesFile=" + os.DevNull}, sub...)
}

// gitModified refuses a filter-declaring repository, then parses the -z
// porcelain output (a rename or copy entry's second path is skipped). Paths
// git ignores ("!!" entries, a whole directory as one entry) come back
// separately: they are scanned for the canary but never listed.
func gitModified(ctx context.Context, l Lease) (changed, ignored []string, err error) {
	if err := refuseGitDrivers(ctx, l); err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, "git", gitArgs(l, "status", "--porcelain", "-z",
		"--untracked-files=all", "--ignored=matching", "--ignore-submodules=all")...)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindInternal, err, "claude: git status in the leased tree")
	}
	changed = []string{}
	fields := bytes.Split(bytes.TrimRight(out, "\x00"), []byte{0})
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		if entry[0] == '!' {
			ignored = append(ignored, string(entry[3:]))
			continue
		}
		changed = append(changed, string(entry[3:]))
		if entry[0] == 'R' || entry[0] == 'C' {
			i++
		}
	}
	return changed, ignored, nil
}

// refuseGitDrivers reads the repository's own config through git (which
// executes nothing to read it) and every attributes file in the tree and
// the git directory, and refuses on any filter driver or filter attribute.
func refuseGitDrivers(ctx context.Context, l Lease) error {
	cmd := exec.CommandContext(ctx, "git", gitArgs(l, "config", "--get-regexp",
		`^filter\..*\.(clean|smudge|process)$`)...)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil && len(bytes.TrimSpace(out)) > 0:
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrGitExecDriver, "claude: git config declares a filter driver")
	case err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1):
		return cascade.Wrapf(cascade.KindInternal, err, "claude: reading the repository's git config")
	}
	files := []string{filepath.Join(l.GitDir, "info", "attributes")}
	walkErr := filepath.WalkDir(l.Root, func(path string, de fs.DirEntry, werr error) error {
		switch {
		case werr != nil:
			return werr
		case de.IsDir() && de.Name() == ".git":
			return filepath.SkipDir
		case de.Name() == ".gitattributes" && de.Type().IsRegular():
			files = append(files, path)
		}
		return nil
	})
	if walkErr != nil {
		return cascade.Wrapf(cascade.KindInternal, walkErr, "claude: walking the tree for attributes")
	}
	for _, f := range files {
		if attributesReferenceFilter(f) {
			return cascade.Wrapf(cascade.KindCapabilityDenied, ErrGitExecDriver, "claude: %s references a filter", filepath.Base(f))
		}
	}
	return nil
}

// attributesReferenceFilter reports whether an attributes file names the
// filter attribute on any line (a missing file names nothing).
func attributesReferenceFilter(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if filterAttr.MatchString(line) {
			return true
		}
	}
	return sc.Err() != nil
}

// scanArtifact checks one modified path's content for the canary without
// following links: a regular file is streamed, a symlink's target text is
// checked, a directory (an untracked nested tree) is walked, and a deleted
// path has nothing to read. Anything else fails closed.
func scanArtifact(ctx context.Context, root, rel string, forms [][]byte) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return unscannable(rel, err)
	case fi.Mode()&fs.ModeSymlink != 0:
		target, rerr := os.Readlink(path)
		if rerr != nil {
			return unscannable(rel, rerr)
		}
		if containsCanary([]byte(target), forms) {
			return ErrCredentialCanary
		}
		return nil
	case fi.IsDir():
		return filepath.WalkDir(path, func(p string, de fs.DirEntry, werr error) error {
			if werr != nil {
				return unscannable(rel, werr)
			}
			sub, _ := filepath.Rel(root, p)
			if containsCanary([]byte(sub), forms) {
				return ErrCredentialCanary
			}
			if de.IsDir() {
				return nil
			}
			return scanArtifact(ctx, root, filepath.ToSlash(sub), forms)
		})
	case !fi.Mode().IsRegular():
		return unscannable(rel, cascade.Newf(cascade.KindInvalidInput, "file mode %v", fi.Mode().Type()))
	}
	return scanRegular(ctx, path, rel, forms)
}

// scanRegular streams one regular file through a canaryScanner. The open
// neither follows a link nor blocks on a FIFO swapped in after the Lstat,
// and the opened handle must still be a regular file.
func scanRegular(ctx context.Context, path, rel string, forms [][]byte) error {
	f, err := os.OpenFile(path, os.O_RDONLY|openNoFollow|openNonBlock, 0)
	if err != nil {
		return unscannable(rel, err)
	}
	defer func() { _ = f.Close() }()
	if fi, serr := f.Stat(); serr != nil || !fi.Mode().IsRegular() {
		return unscannable(rel, serr)
	}
	sc, buf := newCanaryScanner(forms), make([]byte, 64<<10)
	for {
		if cerr := ctx.Err(); cerr != nil {
			return unscannable(rel, cerr)
		}
		n, rerr := f.Read(buf)
		sc.Scan(buf[:n])
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return unscannable(rel, rerr)
		}
	}
	if sc.Hit() {
		return ErrCredentialCanary
	}
	return nil
}

// unscannable fails the run closed on an artifact the scan cannot read.
func unscannable(rel string, err error) error {
	return cascade.Wrapf(cascade.KindIntegrity, err, "claude: artifact %s could not be scanned for the canary", rel)
}
