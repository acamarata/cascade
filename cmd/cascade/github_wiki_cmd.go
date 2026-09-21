// Purpose: `cascade github wiki sync`/`cascade github wiki check`
// (P1-E25-W5-S51-T6, D1) -- the two manifest-declared verbs the
// confirming review's PRE-RULING 1 found compiled into
// plugins/github/manifest.toml but unreachable from the built binary:
// plugin_process_mount.go's processPluginCommands() never carried their
// specs, so `cascade github wiki --help` showed no `wiki` noun at all.
//
// WHY THESE DISPATCH INTO THE PROCESS (unlike github_ci_cmd.go's two
// host-implemented verbs). plugins/github/main.go's own doc comment
// calls this out explicitly: wiki sync/check are "the OPPOSITE case"
// from wait-on-green/merge-on-green, because the git clone/commit/push
// they perform is exec-only inside that process
// (plugins/github/wiki's GitRunner), never a REST call a host-side
// bridge could make directly. So, like merge-on-green's single
// cascade-github.prs.merge call, both verbs mount generically
// (plugin_process_mount.go) and run through the SAME honest
// plugin-host bridge pattern S-51.T3 established
// (internal/plugins/ci_waitmerge_wiring.go): a live process.Handle when
// one exists, or today's typed "no trust-elevation path" refusal
// (internal/plugins/wiki_wiring.go), never a fabricated success.
//
// Inputs: --owner/--repo/--local-dir/--yes flags; an injected resolver
// (production: plugins.NewGitHubWikiCallerForHost) so a test can prove
// the RunE reaches it with the parsed flags without a live process.
// Outputs: the RPC-decoded wiki.SyncResult / drift report printed via
// cmd.Printf. `wiki check` returns a non-nil KindConflict error when
// drift was found, so the process exit code matches
// `cascade-github.wiki.check`'s own verdict (AC5); `wiki sync` returns
// nil on a successful push or a no-op.
// Constraints: mirrors cmd/cascade/github_ci_cmd.go's
// githubCIBridge/productionGitHubCIBridge injection shape. wikiArgs and
// the check reply's wire shape are MIRRORED, not imported, from
// plugins/github's own package-main types (unexported, in a different
// binary) -- the same reasoning internal/ci/waitmerge_dispatch.go's
// header comment gives for mirroring plugins/github/tools' wire shapes.
// SPORT: cmd/cascade:github-wiki-verbs (ADD) -- P1-E25-W5-S51-T6.
package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/wiki"
)

// githubWikiCaller is the minimal transport both wiki verbs need: one RPC
// method call against a live cascade-github process. Structurally
// satisfied by internal/plugins.GitHubPluginCaller -- declared again here
// so this file's injection point (githubWikiCallerResolver) does not
// force every test to import internal/plugins/process just to build a
// fake.
type githubWikiCaller interface {
	Call(ctx context.Context, method string, params []byte) ([]byte, error)
}

// githubWikiCallerResolver resolves the transport fresh on every call --
// the same shape productionGitHubCIBridge's Merge func uses
// plugins.NewGitHubMergeCallerForHost through.
type githubWikiCallerResolver func(ctx context.Context) (githubWikiCaller, error)

// productionGitHubWikiCaller is the PRODUCTION resolver.
func productionGitHubWikiCaller(ctx context.Context) (githubWikiCaller, error) {
	return plugins.NewGitHubWikiCallerForHost(ctx)
}

// githubWikiArgs mirrors plugins/github's unexported wikiArgs json tags
// (wiki_cmd.go, package main of the cascade-github plugin binary --
// unexported and in a different `package main`, so it cannot be
// imported; see this file's header comment).
type githubWikiArgs struct {
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	LocalDir string `json:"local_dir"`
	Yes      bool   `json:"yes"`
}

// githubWikiCheckWireResult mirrors plugins/github's unexported
// wikiCheckResult shape (wiki.DriftResult embedded, plus exit_code).
type githubWikiCheckWireResult struct {
	wiki.DriftResult
	ExitCode int `json:"exit_code"`
}

// githubWikiFlags are the flags both verbs share.
type githubWikiFlags struct {
	owner, repo, localDir string
	yes                   bool
}

// bindShared registers the flags every wiki verb takes.
func (f *githubWikiFlags) bindShared(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.owner, "owner", "", "repository owner (required)")
	cmd.Flags().StringVar(&f.repo, "repo", "", "repository name (required)")
	cmd.Flags().StringVar(&f.localDir, "local-dir", "", "local wiki directory (default: .github/wiki)")
}

// requireOwnerRepo is the flag-level guard both verbs run before ever
// resolving a caller -- an unparseable target must never reach the
// plugin-host bridge.
func requireOwnerRepo(owner, repo string) error {
	if owner == "" || repo == "" {
		return cascade.New(cascade.KindInvalidInput, "cascade github wiki: --owner and --repo are both required")
	}
	return nil
}

// newGitHubWikiSyncCmd builds `cascade github wiki sync`.
func newGitHubWikiSyncCmd(resolve githubWikiCallerResolver) *cobra.Command {
	f := &githubWikiFlags{}
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Push .github/wiki/ to the repository's GitHub wiki (public repos only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGitHubWikiSync(cmd, resolve, f)
		},
	}
	f.bindShared(cmd)
	cmd.Flags().BoolVar(&f.yes, "yes", false, "confirm the push (required under CASCADE_NO_INPUT=1)")
	return cmd
}

// runGitHubWikiSync dispatches one sync call, split from RunE to stay
// under the 50-line function cap.
func runGitHubWikiSync(cmd *cobra.Command, resolve githubWikiCallerResolver, f *githubWikiFlags) error {
	if err := requireOwnerRepo(f.owner, f.repo); err != nil {
		return err
	}
	caller, err := resolve(cmd.Context())
	if err != nil {
		return err
	}
	params, err := json.Marshal(githubWikiArgs{Owner: f.owner, Repo: f.repo, LocalDir: f.localDir, Yes: f.yes})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade github wiki sync: encoding the call")
	}
	raw, err := caller.Call(cmd.Context(), "cascade-github.wiki.sync", params)
	if err != nil {
		return err
	}
	var res wiki.SyncResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "cascade github wiki sync: decoding the response")
	}
	printWikiSyncResult(cmd, res)
	return nil
}

// printWikiSyncResult reports what Sync did in plain text.
func printWikiSyncResult(cmd *cobra.Command, res wiki.SyncResult) {
	if res.NoOp {
		cmd.Printf("no-op: %s\n", res.Reason)
		return
	}
	cmd.Printf("pushed %d changed file(s):\n", len(res.Changed))
	for _, path := range res.Changed {
		cmd.Printf("  %s\n", path)
	}
}

// newGitHubWikiCheckCmd builds `cascade github wiki check`.
func newGitHubWikiCheckCmd(resolve githubWikiCallerResolver) *cobra.Command {
	f := &githubWikiFlags{}
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report drift between .github/wiki/ and the repository's GitHub wiki, read-only",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGitHubWikiCheck(cmd, resolve, f)
		},
	}
	f.bindShared(cmd)
	return cmd
}

// runGitHubWikiCheck dispatches one check call.
func runGitHubWikiCheck(cmd *cobra.Command, resolve githubWikiCallerResolver, f *githubWikiFlags) error {
	if err := requireOwnerRepo(f.owner, f.repo); err != nil {
		return err
	}
	caller, err := resolve(cmd.Context())
	if err != nil {
		return err
	}
	params, err := json.Marshal(githubWikiArgs{Owner: f.owner, Repo: f.repo, LocalDir: f.localDir})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade github wiki check: encoding the call")
	}
	raw, err := caller.Call(cmd.Context(), "cascade-github.wiki.check", params)
	if err != nil {
		return err
	}
	var res githubWikiCheckWireResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "cascade github wiki check: decoding the response")
	}
	return reportWikiDrift(cmd, res)
}

// reportWikiDrift prints the drift report and returns a non-nil error
// when drift was found, so the process exit code matches
// wiki.DriftExitCode's own verdict (AC5).
func reportWikiDrift(cmd *cobra.Command, res githubWikiCheckWireResult) error {
	if res.Clean {
		cmd.Println("no drift")
		return nil
	}
	for _, path := range res.Added {
		cmd.Printf("added:   %s\n", path)
	}
	for _, path := range res.Removed {
		cmd.Printf("removed: %s\n", path)
	}
	for _, path := range res.Changed {
		cmd.Printf("changed: %s\n", path)
	}
	return cascade.Newf(cascade.KindConflict, "cascade github wiki check: %d file(s) differ from the remote wiki",
		len(res.Added)+len(res.Removed)+len(res.Changed))
}
