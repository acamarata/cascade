package context

import (
	"context"
	"slices"
	"strings"
)

// Purpose: GitRemoteURL is the one production repo.RemoteURLFunc (the CI
//   ledger route): `git remote get-url origin` run in root
//   through the A-T2 exec allowlist (internal/build/egress_allow.go
//   already admits internal/context).
// Inputs: a context.Context and root, the repository directory to query.
// Outputs: the trimmed origin URL, or "" when root has no origin remote,
//   root is not a repository, or git fails for any other reason -- a
//   purely local repository is not an error, so this never returns one.
// Constraints: bounded by gitExecTimeout (5s) independent of ctx's deadline,
//   plus gitWaitDelay for a child that leaves stdout open; every variable
//   `git rev-parse --local-env-vars` names (GIT_DIR, GIT_WORK_TREE,
//   GIT_COMMON_DIR, ...) is stripped from the child's environment so a
//   caller's own git context never leaks into this lookup.
// SPORT: context/repo-identity/ADD.

// GitRemoteURL returns root's configured "origin" remote URL, or "" when
// root has no origin remote or git fails (never an error).
func GitRemoteURL(ctx context.Context, root string) string {
	ctx, cancel := context.WithTimeout(ctx, gitExecTimeout)
	defer cancel()

	out, err := gitOutput(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitLocalEnvVars is what `git rev-parse --local-env-vars` prints: every
// variable that points git at a repository, index, object store or config
// other than the one found from the working directory.
// TestGitLocalEnvVarsCoverLiveGit compares it with the installed git.
var gitLocalEnvVars = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT", "GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_PREFIX",
	"GIT_SHALLOW_FILE", "GIT_COMMON_DIR",
}

// stripGitDirEnv returns env without any gitLocalEnvVars entry, so the
// child git invocation resolves root's own repository rather than one a
// caller's environment happens to point at.
func stripGitDirEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(gitLocalEnvVars, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
