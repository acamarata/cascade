// Purpose: the two HOST-IMPLEMENTED verbs the cascade-github manifest
//
//	declares -- `cascade github ci wait` and `cascade github ci
//	merge-on-green` -- plus the composition that gives internal/ci's
//	WaitOnGreen/MergeOnGreen their real collaborators.
//
// WHY THESE ARE HOST-IMPLEMENTED. wait-on-green polls api.github.com
//
//	through the HOST's own never-pay-guarded internal/ci.Client; it never
//	enters the cascade-github process at all. merge-on-green is the only
//	half that needs the plugin: its one call is the manifest's
//	cascade-github.prs.merge tool over the O/S-31.T3 stdio transport
//	(R-21.270), reached through internal/plugins' bridge. So the verbs mount
//	generically (plugin_process_mount.go) but RUN here.
//
// Inputs: flags; the process environment (the API token, via
//
//	ci.WaitDepsFromEnv); the runtime store (the grant store an L3 merge is
//	authorized against).
//
// Outputs: exit zero on green (and on a completed merge), a typed non-zero
//
//	refusal otherwise. `merge-on-green` refuses TODAY with the typed
//	elevation prerequisite from internal/plugins, because no process-tier
//	plugin can be launched in this build; that refusal is the honest answer,
//	not a placeholder success.
//
// Constraints: no bare time.Now (forbidigo); the bridge is injected so a
//
//	test can prove a RunE reaches internal/ci without a network or a
//	keychain.
//
// SPORT: cmd/cascade:github-ci-verbs (ADD) -- P1-E25-W5-S51-T3.
package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// githubCISubject is the acting subject the CLI evaluates an unattended
// merge as. A grant is written against this subject, never against "any
// caller".
var githubCISubject = policy.Subject{Kind: policy.SubjectUser, ID: "github-ci"}

// githubCIBridge is the internal/ci entry point the two verbs reach. It is
// a struct of functions, not a direct call, for one reason: a test must be
// able to prove that a RunE reaches THIS seam (and reaches it with the
// flags it parsed) without opening a socket or a keychain.
type githubCIBridge struct {
	Wait  func(ctx context.Context, opts ci.WaitOptions) (ci.WaitResult, error)
	Merge func(ctx context.Context, opts ci.MergeOptions) (ci.MergeResult, error)
}

// productionGitHubCIBridge is the real bridge: internal/ci, composed here.
func productionGitHubCIBridge() githubCIBridge {
	return githubCIBridge{Wait: hostWaitOnGreen, Merge: hostMergeOnGreen}
}

// hostedProcessVerbs is plugin_process_mount.go's host-implementation map.
func hostedProcessVerbs() map[string]func() *cobra.Command {
	bridge := productionGitHubCIBridge()
	return map[string]func() *cobra.Command{
		"github-ci-wait":           func() *cobra.Command { return newGitHubCIWaitCmd(bridge) },
		"github.ci.merge-on-green": func() *cobra.Command { return newGitHubCIMergeOnGreenCmd(bridge) },
	}
}

// githubCIFlags are the flags both verbs share, plus the two merge-only
// ones. Declared once so the two commands cannot drift apart.
type githubCIFlags struct {
	repo     string
	ref      string
	timeout  time.Duration
	required []string
	skipped  []string
	pr       int
	yes      bool
}

// bindWaitFlags registers the wait flags on cmd.
func (f *githubCIFlags) bindWaitFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.repo, "repo", "", "owner/repo to watch")
	cmd.Flags().StringVar(&f.ref, "ref", "", "branch name or head SHA to watch")
	cmd.Flags().DurationVar(&f.timeout, "timeout", ci.DefaultWaitTimeout, "how long to wait before giving up")
	cmd.Flags().StringSliceVar(&f.required, "required-check", nil,
		"check names that must succeed (default: every non-skipped job of the completed run)")
	cmd.Flags().StringSliceVar(&f.skipped, "allow-skipped", nil,
		"required check names whose `skipped` conclusion counts as green")
}

// waitOptions renders the parsed flags as a ci.WaitOptions.
func (f *githubCIFlags) waitOptions() (ci.WaitOptions, error) {
	owner, repo, err := splitOwnerRepo(f.repo)
	if err != nil {
		return ci.WaitOptions{}, err
	}
	return ci.WaitOptions{
		Owner: owner, Repo: repo, Ref: f.ref,
		RequiredChecks: f.required, AllowSkipped: f.skipped, Timeout: f.timeout,
	}, nil
}

// splitOwnerRepo parses the --repo flag. An unparseable value refuses; it
// is never guessed from the working directory here, because a merge is
// irreversible and "whatever repo you happened to be in" is not consent.
func splitOwnerRepo(spec string) (owner, repo string, err error) {
	parts := strings.Split(strings.TrimSpace(spec), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", cascade.Newf(cascade.KindInvalidInput,
			"--repo must be owner/repo, got %q", spec)
	}
	return parts[0], parts[1], nil
}

// newGitHubCIWaitCmd builds `cascade github ci wait`.
func newGitHubCIWaitCmd(bridge githubCIBridge) *cobra.Command {
	flags := &githubCIFlags{}
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Block until every required GitHub Actions check for a repo+ref reports success",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := flags.waitOptions()
			if err != nil {
				return err
			}
			result, err := bridge.Wait(cmd.Context(), opts)
			if err != nil {
				return err
			}
			cmd.Printf("green: %s/%s@%s run %d (%d checks, %s)\n",
				result.Owner, result.Repo, result.Ref, result.RunID, len(result.Checks), result.Elapsed)
			return nil
		},
	}
	flags.bindWaitFlags(cmd)
	return cmd
}

// newGitHubCIMergeOnGreenCmd builds `cascade github ci merge-on-green`.
func newGitHubCIMergeOnGreenCmd(bridge githubCIBridge) *cobra.Command {
	flags := &githubCIFlags{}
	cmd := &cobra.Command{
		Use:   "merge-on-green",
		Short: "Merge a pull request once its required checks are green (L3, policy-gated)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := flags.mergeOptions()
			if err != nil {
				return err
			}
			result, err := bridge.Merge(cmd.Context(), opts)
			if err != nil {
				return err
			}
			cmd.Printf("merged %s/%s #%d at %s\n", opts.Owner, opts.Repo, opts.PR, result.SHA)
			return nil
		},
	}
	flags.bindWaitFlags(cmd)
	cmd.Flags().IntVar(&flags.pr, "pr", 0, "pull request number to merge")
	cmd.Flags().BoolVar(&flags.yes, "yes", false,
		"confirm the unattended merge (required: this verb performs an irreversible remote action)")
	return cmd
}

// mergeOptions renders the parsed flags as a ci.MergeOptions. --yes is
// mandatory: an L3 external side effect never runs because a flag was
// defaulted.
func (f *githubCIFlags) mergeOptions() (ci.MergeOptions, error) {
	owner, repo, err := splitOwnerRepo(f.repo)
	if err != nil {
		return ci.MergeOptions{}, err
	}
	if !f.yes {
		return ci.MergeOptions{}, cascade.New(cascade.KindInvalidInput,
			"cascade github ci merge-on-green needs --yes: merging a pull request is an irreversible action "+
				"on a remote service and is never performed on a defaulted flag")
	}
	return ci.MergeOptions{
		Owner: owner, Repo: repo, PR: f.pr, Ref: f.ref,
		MergeMethod: "squash", Subject: githubCISubject,
		// The CLI thread carries no material above internal, and
		// MergeOptions.dataClass floors it at internal in any case.
		DataClass: policy.DataClassInternal,
	}, nil
}

// hostWaitOnGreen is the production Wait bridge.
func hostWaitOnGreen(ctx context.Context, opts ci.WaitOptions) (ci.WaitResult, error) {
	deps, err := ci.WaitDepsFromEnv(newGitHubTokenDoer, os.Getenv, runtime.NewSystemClock())
	if err != nil {
		return ci.WaitResult{}, err
	}
	return ci.WaitOnGreen(ctx, deps, opts)
}

// hostMergeOnGreen is the production Merge bridge: the plugin-host
// prerequisite FIRST (there is no point authorizing an action that cannot
// be performed), then the wait, then the L3-gated merge.
func hostMergeOnGreen(ctx context.Context, opts ci.MergeOptions) (ci.MergeResult, error) {
	caller, err := plugins.NewGitHubMergeCallerForHost(ctx)
	if err != nil {
		return ci.MergeResult{}, err
	}
	waitDeps, err := ci.WaitDepsFromEnv(newGitHubTokenDoer, os.Getenv, runtime.NewSystemClock())
	if err != nil {
		return ci.MergeResult{}, err
	}
	engine, log, closeStore, err := githubCIPolicy()
	if err != nil {
		return ci.MergeResult{}, err
	}
	defer closeStore()
	wait, err := ci.WaitOnGreen(ctx, waitDeps, ci.WaitOptions{Owner: opts.Owner, Repo: opts.Repo, Ref: opts.Ref})
	if err != nil {
		return ci.MergeResult{}, err
	}
	deps := ci.MergeDeps{Engine: engine, Caller: caller, HeadSHA: ci.ClientHeadSHA(waitDeps.Client), Audit: log}
	return ci.MergeOnGreen(ctx, deps, opts, wait)
}

// githubCICapabilities are the two capabilities the GitHub CI verbs
// evaluate against, and they are DISTINCT on purpose (D4).
//
// Both are ClassExternalSideEffect, which is what puts an unattended merge
// on the 06 §5.15 L3 rung: MergeOnGreen's EvalRequest is CommandLess, so
// R-14.211 takes the rung from the registered capability's own class rather
// than from shell-command text -- meaning this registration IS the L3
// classification, not a label on it. An unregistered capability denies, so
// without this list `cascade policy grant cascade-github.prs.merge_on_green`
// would have nothing to grant against.
//
// This function is the one source; bootCapabilities (the daemon's
// composition root) appends it, and githubCIPolicy below seeds the CLI's
// engine from it, so the two can never disagree about the class.
func githubCICapabilities() []policy.Capability {
	return []policy.Capability{
		{
			Name:          ci.MergeCapability,
			Desc:          "merge a pull request on GitHub",
			DefaultPolicy: policy.ClassExternalSideEffect,
		},
		{
			Name:          ci.MergeOnGreenCapability,
			Desc:          "merge a pull request UNATTENDED once its required checks are green",
			DefaultPolicy: policy.ClassExternalSideEffect,
		},
	}
}

// githubCIPolicy builds the real policy engine and audit log an unattended
// merge is decided by, over the runtime store.
//
// It reuses openMCPPolicyStore because that is this binary's ONE
// platform-split opener for the runtime store (daemon_unix_store.go's
// openRuntimeStore is !windows, and the windows half is the documented
// tier-2 refusal). The name predates this second caller; what matters is
// that the grants an operator wrote with `cascade policy grant` are the
// ones read here, rather than a second, empty store.
func githubCIPolicy() (*policy.Engine, audit.Writer, func(), error) {
	clock := runtime.NewSystemClock()
	store, closeStore, err := openMCPPolicyStore(lazyPaths{}, clock)
	if err != nil {
		return nil, nil, nil, err
	}
	registry := policy.NewMemoryRegistry()
	for _, capability := range githubCICapabilities() {
		if err := registry.Add(context.Background(), capability); err != nil {
			closeStore()
			return nil, nil, nil, err
		}
	}
	grants, err := policy.NewStoreGrants(store, registry, clock)
	if err != nil {
		closeStore()
		return nil, nil, nil, err
	}
	engine, err := policy.NewEngine(registry, grants, policy.NewController(nil))
	if err != nil {
		closeStore()
		return nil, nil, nil, err
	}
	return engine, audit.New(store, clock, nil), closeStore, nil
}
