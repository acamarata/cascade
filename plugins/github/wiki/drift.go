// Purpose (this file): CheckDrift — `cascade github wiki check`, the
//
//	read-only drift gate. It clones the remote wiki to an isolated tempdir
//	(never pushes) and diffs it against the local .github/wiki/ directory.
//
// Inputs: DriftOptions (owner, repo, the local wiki directory, the OAuth
//
//	token, and the same GitRunner/MkdirTemp seams Sync uses).
//
// Outputs: a DriftResult naming every added/removed/changed file, sorted;
//
//	Clean is true only when all three are empty.
//
// Constraints: read-only by construction — this file never calls
//
//	commitAll or pushWiki, and never asks for confirmation: drift
//	detection is 06-FORGE-SPEC.md §5.15's L1-L2 (reading a remote into a
//	tempdir, no mutation of anything the operator owns). A CI check step
//	calls this without --yes and without CASCADE_NO_INPUT ever mattering.
//
// SPORT: plugins/github/wiki:drift (ADD) — P1-E25-W5-S51-T6.

package wiki

import "context"

// DriftOptions configures one CheckDrift call.
type DriftOptions struct {
	// Owner and Repo name the repository whose wiki is being checked.
	Owner, Repo string
	// LocalDir is the local .github/wiki/ directory to compare against.
	LocalDir string
	// Token authorizes the (read-only) clone. Public wikis need none;
	// a higher API rate limit is the only reason to supply it here.
	Token string
	// Runner performs git subprocess calls. Nil uses the real git binary.
	Runner GitRunner
	// MkdirTemp creates the isolated clone directory. Nil uses a real
	// temp directory; tests inject a t.TempDir()-backed value.
	MkdirTemp func() (dir string, cleanup func(), err error)
}

// DriftResult reports how the local wiki compares to the remote one.
type DriftResult struct {
	// Added lists paths present locally but not on the remote wiki.
	Added []string
	// Removed lists paths present on the remote wiki but not locally.
	Removed []string
	// Changed lists paths present in both with different content.
	Changed []string
	// Clean is true only when Added, Removed and Changed are all empty.
	Clean bool
}

// CheckDrift implements `cascade github wiki check`. It never mutates the
// remote wiki, the local directory, or asks for confirmation — the three
// properties that make it safe to run unattended in CI.
func CheckDrift(ctx context.Context, opts DriftOptions) (DriftResult, error) {
	if err := validateOwnerRepo(opts.Owner, opts.Repo); err != nil {
		return DriftResult{}, err
	}
	if err := requireGit(); err != nil {
		return DriftResult{}, err
	}

	tmp, cleanup, err := makeTempDir(opts.MkdirTemp)
	if err != nil {
		return DriftResult{}, err
	}
	defer cleanup()

	runner := newRunner(opts.Runner)
	url := resolveWikiURL(opts.Owner, opts.Repo)
	if err := cloneWiki(ctx, runner, url, tmp, gitAuthEnv(opts.Token)); err != nil {
		return DriftResult{}, err
	}

	local, err := readTree(opts.LocalDir)
	if err != nil {
		return DriftResult{}, err
	}
	remote, err := readTree(tmp)
	if err != nil {
		return DriftResult{}, err
	}
	added, removed, changed := diffTrees(local, remote)
	return DriftResult{
		Added:   added,
		Removed: removed,
		Changed: changed,
		Clean:   len(added) == 0 && len(removed) == 0 && len(changed) == 0,
	}, nil
}

// DriftExitCode maps a DriftResult onto the CLI exit code `cascade github
// wiki check` reports: 0 for no drift, 1 otherwise. Exported so the host
// CLI layer (when it mounts this command) and this package's own tests
// share one definition of "non-zero on drift" rather than each guessing.
func DriftExitCode(res DriftResult) int {
	if res.Clean {
		return 0
	}
	return 1
}
