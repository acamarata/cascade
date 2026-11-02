// Purpose: the pre-spawn checks of the host shim that look at the leased
// tree and the job spec: the minimal-tree assertion, the secret scan over the
// process detector, and the tool policy the envelope allows.
// Inputs: a worktree root, its declared WorktreeScope, an envelope tool list.
// Outputs: nil, or a typed refusal naming the offending path or policy.
// Constraints: AssertMinimalWorktree allows only the declared prefixes, their
// ancestor directories, .git and .claude; a symlink must resolve inside the
// root and onto an allowed path, and a dangling link is refused. RunPreSpawnScan
// scans through provider.PreSpawnScan with ProcessDetectorMatch and no pattern
// of its own; a hit or any scan failure is a refusal (fail closed). Refusals
// name paths and classes, never file content.
// SPORT: internal.providers.agents/ADD (P1-AGT-03, contract:driver-custody-shim).

package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// envelopeToolPrefix names the tool entries a delegation envelope carries.
const envelopeToolPrefix = "agent.tools."

// ToolPolicyFromEnvelope maps an envelope's tool list to the one tool policy
// it allows: "agent.tools.all" is all, "agent.tools.readonly" is readonly,
// anything else is none. The widest entry wins.
func ToolPolicyFromEnvelope(tools []string) string {
	switch {
	case slices.Contains(tools, envelopeToolPrefix+provider.ToolPolicyAll):
		return provider.ToolPolicyAll
	case slices.Contains(tools, envelopeToolPrefix+provider.ToolPolicyReadonly):
		return provider.ToolPolicyReadonly
	}
	return provider.ToolPolicyNone
}

// checkToolPolicy refuses a spec whose tool policy differs from the envelope's.
func checkToolPolicy(specPolicy string, tools []string) error {
	got, err := provider.ResolveToolPolicy(specPolicy)
	if err != nil {
		return err
	}
	if want := ToolPolicyFromEnvelope(tools); got != want {
		return cascade.Newf(cascade.KindPolicyDenied,
			"agents: tool policy %q is not the envelope's %q; a wider or different policy is refused", got, want)
	}
	return nil
}

// scopePrefixes returns the cleaned, slash-form prefixes of s; a prefix that
// is absolute or climbs out of the root is refused. No prefix at all returns
// an empty list (the tree may then hold nothing but .git and .claude).
func scopePrefixes(s provider.WorktreeScope) ([]string, error) {
	var out []string
	for _, p := range append(slices.Clone(s.InScopePrefixes), s.DeclaredDeps...) {
		c := path.Clean(filepath.ToSlash(p))
		if path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") || filepath.IsAbs(p) {
			return nil, cascade.Newf(cascade.KindPolicyDenied, "agents: scope prefix %q leaves the worktree", p)
		}
		out = append(out, c)
	}
	return out, nil
}

// allowedRel reports whether rel (slash form, "." for the root) may exist in
// the tree: inside a prefix, or an ancestor directory of one when isDir.
func allowedRel(rel string, isDir bool, prefixes []string) bool {
	if rel == "." || rel == ".git" || rel == ".claude" || strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, ".claude/") {
		return true
	}
	for _, p := range prefixes {
		if p == "." || rel == p || strings.HasPrefix(rel, p+"/") || (isDir && strings.HasPrefix(p, rel+"/")) {
			return true
		}
	}
	return false
}

// AssertMinimalWorktree refuses a tree that holds any path outside the
// declared scope plus .git and .claude (KindPolicyDenied naming it), a symlink
// that escapes root or lands on a path outside the scope, and a link that
// cannot be resolved.
func AssertMinimalWorktree(root string, s provider.WorktreeScope) error {
	prefixes, err := scopePrefixes(s)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, err, "agents: worktree root %q cannot be resolved", root)
	}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return cascade.Wrapf(cascade.KindPolicyDenied, err, "agents: worktree walk failed at %q", p)
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return cascade.Wrap(cascade.KindPolicyDenied, rerr, "agents: worktree path is not under the root")
		}
		rel = filepath.ToSlash(rel)
		if !allowedRel(rel, d.IsDir(), prefixes) {
			return cascade.Newf(cascade.KindPolicyDenied, "agents: %s is outside the declared worktree scope", rel)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return checkLink(p, rel, realRoot, prefixes)
		}
		if d.IsDir() && (rel == ".git" || rel == ".claude") {
			return filepath.SkipDir
		}
		return nil
	})
	return walkErr
}

// checkLink requires a symlink to resolve inside realRoot onto an allowed path.
func checkLink(full, rel, realRoot string, prefixes []string) error {
	target, err := filepath.EvalSymlinks(full)
	if err != nil {
		return cascade.Newf(cascade.KindPolicyDenied, "agents: symlink %s cannot be resolved", rel)
	}
	trel, err := filepath.Rel(realRoot, target)
	if err != nil || trel == ".." || strings.HasPrefix(filepath.ToSlash(trel), "../") {
		return cascade.Newf(cascade.KindPolicyDenied, "agents: symlink %s escapes the worktree", rel)
	}
	if !allowedRel(filepath.ToSlash(trel), true, prefixes) {
		return cascade.Newf(cascade.KindPolicyDenied, "agents: symlink %s points outside the declared worktree scope", rel)
	}
	return nil
}

// RunPreSpawnScan scans each declared prefix with provider.PreSpawnScan over
// the process detector. journal, when set, receives the credential class and
// the prefix of a hit (never content). A hit is provider.ErrPreSpawnSecretFound
// by identity; a detector or scan failure wraps it (fail closed).
func RunPreSpawnScan(root string, s provider.WorktreeScope, journal func(class, path string)) error {
	match, err := ProcessDetectorMatch()
	if err != nil {
		return cascade.Wrap(cascade.KindCapabilityDenied, provider.ErrPreSpawnSecretFound,
			"agents: pre-spawn scan refused: the secret detector is unavailable")
	}
	prefixes, err := scopePrefixes(s)
	if err != nil {
		return err
	}
	if len(prefixes) == 0 {
		prefixes = []string{"."}
	}
	for _, p := range prefixes {
		var class string
		hit := func(content []byte) (string, bool) {
			c, ok := match(content)
			if ok {
				class = c
			}
			return c, ok
		}
		serr := provider.PreSpawnScan(root, provider.WorktreeScope{InScopePrefixes: []string{p}}, hit)
		if serr == nil {
			continue
		}
		if serr == provider.ErrPreSpawnSecretFound { //nolint:errorlint // identity: errors.Is compares Kind only
			if journal != nil {
				journal(class, p)
			}
			return serr
		}
		return cascade.Wrap(cascade.KindCapabilityDenied, provider.ErrPreSpawnSecretFound,
			"agents: pre-spawn scan refused: it could not finish under "+p)
	}
	return nil
}

// mintJobID returns "agt-" plus 16 random hex characters.
func mintJobID() (provider.AgentJobID, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "agents: a job id could not be generated")
	}
	return provider.AgentJobID("agt-" + hex.EncodeToString(b)), nil
}

// checkExecutable requires the driver to report exactly the configured
// executable (or its symlink-resolved form) and a binding-listed basename.
func checkExecutable(drv provider.AgentProvider, want string, b Binding) error {
	rep, ok := drv.(provider.ExecutableReporter)
	if !ok {
		return cascade.Newf(cascade.KindPolicyDenied, "agents: the %s driver does not report its executable", b.Harness)
	}
	got := rep.Executable()
	resolved, err := filepath.EvalSymlinks(want)
	if got == "" || (got != want && (err != nil || got != resolved)) {
		return cascade.Newf(cascade.KindPolicyDenied, "agents: the %s driver's executable is not the configured one", b.Harness)
	}
	base := strings.TrimSuffix(filepath.Base(got), ".exe")
	for _, name := range b.Binaries {
		if base == strings.TrimSuffix(name, ".exe") {
			return nil
		}
	}
	return cascade.Newf(cascade.KindPolicyDenied, "agents: the %s executable name is not one the binding lists", b.Harness)
}

// checkTree asserts the minimal tree and runs the secret scan.
func (r *spawnRun) checkTree(context.Context) error {
	if err := AssertMinimalWorktree(r.job.WorktreeRoot, r.job.Scope); err != nil {
		return err
	}
	return RunPreSpawnScan(r.job.WorktreeRoot, r.job.Scope, func(class, path string) {
		slog.Warn("agent pre-spawn scan refused a spawn", "harness", string(r.b.Harness), "class", class, "path", path)
	})
}
