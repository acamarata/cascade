// Purpose: merge-on-green (P1-E25-W5-S51-T3, task 3-4): given a WaitResult
// that already resolved green, classify the merge as L3, refuse without an
// explicit merge-on-green grant, re-check that the ref has not moved, and
// dispatch cascade-github.prs.merge over the plugin-host call path.
//
// Inputs: MergeOptions (owner/repo/PR/ref/merge method/acting subject and
// that subject's data-sensitivity tier), a WaitResult from THIS SAME ref
// and head SHA (rebindCheck refuses a stale one), and MergeDeps (a real
// *policy.Engine, an injected MergeCaller, an injected HeadSHAFetcher, and
// an audit.Writer).
//
// Outputs: a MergeResult{Merged:true, SHA} on success; a typed, audited
// refusal (KindConflict on a stale/unresolved/moved wait, KindPolicyDenied
// on a missing grant, KindUnavailable/KindIntegrity/KindConflict on a
// failed or refused GitHub call) otherwise. MergeOnGreen NEVER calls the
// plugin host on any refusal path -- every gate runs strictly before
// Caller.Call -- and the policy decision is AUDITED BEFORE the call, so a
// kill mid-dispatch can never leave an executed L3 side effect with no
// record of who authorized it.
//
// Constraints: internal/ci is core and may not import plugins/** (Art.10.2),
// so nothing here imports plugins/github/tools: waitmerge_dispatch.go's
// mergeWireParams/mergeWireResult mirror that package's Args/MergeResult JSON
// tags (owner/repo/number/merge_method, sha/merged/message) as a plain wire
// contract instead. Classification runs through the REAL
// I/S-17.T3+I/S-17.T4+I/S-17.T1 seam (policy.Engine.Evaluate), never a
// re-derived copy: CommandLess=true takes the rung from the registered
// capability's own ActionClass (R-14.211), so MergeOnGreenCapability must
// be registered ClassExternalSideEffect (L3) -- cmd/cascade's
// bootCapabilities does that in production -- and this file asserts the
// level it gets back rather than trusting it.
//
// PLUGIN-HOST CALL PATH (R-21.270): MergeCaller's method set is IDENTICAL
// to internal/plugins/process.(*Handle).Call's (ctx, method string, params
// []byte) ([]byte, error), so the real O/S-31.T3 Handle satisfies it with
// zero adapter code -- see internal/plugins/ci_waitmerge_wiring.go. The
// merge is dispatched as a JSON-RPC request to "cascade-github.prs.merge",
// never through model.execute, which 02-TARGET-STRUCTURE reserves as the
// ONLY model door for plugins.
//
// SPORT: internal.ci.MergeOnGreen/ADDED, internal.ci.MergeOptions/ADDED,
//
//	internal.ci.MergeDeps/ADDED, internal.ci.MergeCaller/ADDED,
//	internal.ci.MergeResult/ADDED (P1-E25-W5-S51-T3).

package ci

import (
	"context"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MergeCapability is the capability a HUMAN-driven merge of a pull request
// evaluates against. merge-on-green deliberately does NOT use it: see
// MergeOnGreenCapability.
const MergeCapability = "cascade-github.prs.merge"

// MergeOnGreenCapability is the capability UNATTENDED merge-on-green
// evaluates against, and it is a DISTINCT capability from MergeCapability
// on purpose (D4). A grant issued so an operator could merge one pull
// request themselves must never silently authorize a loop that merges
// whatever goes green next; AC4's words are "an explicit merge-on-green=true
// policy grant", and two capability names is the only way a grant store can
// tell the two apart. Registered ClassExternalSideEffect (L3) by
// cmd/cascade's bootCapabilities.
const MergeOnGreenCapability = "cascade-github.prs.merge_on_green"

// mergeToolMethod is the JSON-RPC method name Handle.Call dispatches:
// PluginName + "." + the manifest tool name (main.go's toolReply prefix
// convention).
const mergeToolMethod = "cascade-github.prs.merge"

// mergeDataCeiling is the sensitivity a merge's DESTINATION tolerates.
// api.github.com is the operator's own code host: internal material (an
// owner, a repo, a PR number) belongs there, and anything the calling
// thread classifies above internal does not. Declaring the ceiling is what
// makes policy layer 0 run at all -- an unset LaneMaxDataClass means "no
// external destination", which is false for an L3 GitHub write (06 §5.16).
const mergeDataCeiling = policy.DataClassInternal

// MergeCaller performs one JSON-RPC round trip against a running
// process-tier plugin. Declared locally (not imported from
// internal/plugins/process, which internal/ci may not import either) so
// *process.Handle satisfies it by having the identical method, matching
// this file's Args-mirroring reasoning above.
type MergeCaller interface {
	Call(ctx context.Context, method string, params []byte) ([]byte, error)
}

// HeadSHAFetcher re-reads the CURRENT head SHA of owner/repo at ref. It is
// the TOCTOU backstop (D8): the wait resolved for one revision, and this
// says whether that is still the revision a merge would land.
type HeadSHAFetcher func(ctx context.Context, owner, repo, ref string) (string, error)

// MergeOptions is one merge-on-green request.
type MergeOptions struct {
	Owner, Repo string
	PR          int
	// Ref is the branch or head SHA the wait was performed against.
	Ref         string
	MergeMethod string
	Subject     policy.Subject
	// DataClass is the calling thread's sensitivity tier. 06 §5.16 says
	// the action inherits it, with internal as the FLOOR, which is what
	// dataClass below implements; an unset value therefore reads as
	// internal rather than as "unclassified".
	DataClass policy.DataClass
}

func (o MergeOptions) validate() error {
	if strings.TrimSpace(o.Owner) == "" || strings.TrimSpace(o.Repo) == "" {
		return cascade.New(cascade.KindInvalidInput, "ci: merge-on-green requires an owner and a repo")
	}
	if o.PR <= 0 {
		return cascade.New(cascade.KindInvalidInput, "ci: merge-on-green requires a positive PR number")
	}
	if strings.TrimSpace(o.Ref) == "" {
		return cascade.New(cascade.KindInvalidInput, "ci: merge-on-green requires the ref it waited on")
	}
	if err := o.Subject.Validate(); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "ci: merge-on-green requires a valid acting subject")
	}
	return nil
}

// dataClass is 06 §5.16's "minimum internal" inheritance.
func (o MergeOptions) dataClass() policy.DataClass {
	if o.DataClass > policy.DataClassInternal {
		return o.DataClass
	}
	return policy.DataClassInternal
}

// MergeDeps carries MergeOnGreen's collaborators. All four are required:
// Audit is not optional here (unlike runner.go's Deps.Journal) because a
// logged policy event is itself an acceptance criterion of this L3 action,
// and HeadSHA is not optional because a merge with no way to re-read the
// target would be exactly the stale-success hole D8 closes.
type MergeDeps struct {
	Engine  *policy.Engine
	Caller  MergeCaller
	HeadSHA HeadSHAFetcher
	Audit   audit.Writer
}

func (d MergeDeps) validate() error {
	switch {
	case d.Engine == nil:
		return cascade.New(cascade.KindInvalidInput, "ci: merge-on-green requires a non-nil policy Engine")
	case d.Caller == nil:
		return cascade.New(cascade.KindInvalidInput, "ci: merge-on-green requires a non-nil MergeCaller")
	case d.HeadSHA == nil:
		return cascade.New(cascade.KindInvalidInput, "ci: merge-on-green requires a non-nil HeadSHAFetcher")
	case d.Audit == nil:
		return cascade.New(cascade.KindInvalidInput, "ci: merge-on-green requires a non-nil audit.Writer")
	}
	return nil
}

// MergeResult is merge-on-green's outcome on a successful merge.
type MergeResult struct {
	Merged bool
	SHA    string
}

// MergeOnGreen refuses to merge without an explicit merge-on-green grant,
// and never calls the plugin host on any refusal path.
func MergeOnGreen(ctx context.Context, deps MergeDeps, opts MergeOptions, wait WaitResult) (MergeResult, error) {
	if err := waitPlatformRefusal(); err != nil {
		return MergeResult{}, err
	}
	if err := deps.validate(); err != nil {
		return MergeResult{}, err
	}
	if err := opts.validate(); err != nil {
		return MergeResult{}, err
	}
	if err := rebindCheck(opts, wait); err != nil {
		return MergeResult{}, err
	}
	outcome, err := deps.authorize(ctx, opts)
	if err != nil {
		return MergeResult{}, err
	}
	// The head re-check runs as LATE as possible: immediately before the
	// irreversible call, after authorization, so the window between the
	// observation and the merge is as small as this process can make it.
	if err := deps.recheckHead(ctx, opts, wait, outcome); err != nil {
		return MergeResult{}, err
	}
	deps.record(ctx, opts, audit.KindPolicyDecide, outcome, policy.VerdictAllow, "dispatching "+mergeToolMethod)
	result, callErr := deps.callMerge(ctx, opts)
	if callErr != nil {
		deps.record(ctx, opts, audit.KindPolicyRoute, outcome, policy.VerdictAllow, "merge call failed: "+callErr.Error())
		return MergeResult{}, callErr
	}
	deps.record(ctx, opts, audit.KindPolicyRoute, outcome, policy.VerdictAllow, "merged "+result.SHA)
	return result, nil
}

// authorize runs the real policy evaluation and audits every refusal.
func (d MergeDeps) authorize(ctx context.Context, opts MergeOptions) (policy.EvalOutcome, error) {
	outcome, err := d.Engine.Evaluate(ctx, mergeEvalRequest(opts))
	if err != nil {
		d.record(ctx, opts, audit.KindPolicyDecide, outcome, policy.VerdictDeny, "evaluate failed: "+err.Error())
		return outcome, err
	}
	if outcome.Level != policy.L3 {
		refusal := cascade.Newf(cascade.KindInternal,
			"ci: merge-on-green classified at %s, want L3 -- refusing rather than merge under the wrong rung", outcome.Level)
		d.record(ctx, opts, audit.KindPolicyDecide, outcome, policy.VerdictDeny, refusal.Error())
		return outcome, refusal
	}
	if outcome.Verdict != policy.VerdictAllow {
		refusal := cascade.Newf(cascade.KindPolicyDenied,
			"ci: merge-on-green refused for %s: no %s grant (%s)", opts.Subject, MergeOnGreenCapability, outcome.Reason)
		d.record(ctx, opts, audit.KindPolicyDecide, outcome, policy.VerdictDeny, refusal.Error())
		return outcome, refusal
	}
	return outcome, nil
}

// recheckHead is D8's TOCTOU guard.
func (d MergeDeps) recheckHead(ctx context.Context, opts MergeOptions, wait WaitResult, outcome policy.EvalOutcome) error {
	current, err := d.HeadSHA(ctx, opts.Owner, opts.Repo, opts.Ref)
	if err != nil {
		refusal := cascade.Wrapf(cascade.KindUnavailable, err,
			"ci: merge-on-green could not re-read %s@%s's head before merging", opts.Owner+"/"+opts.Repo, opts.Ref)
		d.record(ctx, opts, audit.KindPolicyDecide, outcome, policy.VerdictDeny, refusal.Error())
		return refusal
	}
	if current == wait.HeadSHA {
		return nil
	}
	refusal := cascade.Newf(cascade.KindConflict,
		"ci: merge-on-green refuses: %s@%s is now at %s, not the %s that went green",
		opts.Owner+"/"+opts.Repo, opts.Ref, current, wait.HeadSHA)
	d.record(ctx, opts, audit.KindPolicyDecide, outcome, policy.VerdictDeny, refusal.Error())
	return refusal
}

// rebindCheck refuses a WaitResult that did not resolve green, carries no
// head SHA to re-check against, or resolved for a different target than
// opts names.
func rebindCheck(opts MergeOptions, wait WaitResult) error {
	if !wait.Passed {
		return cascade.New(cascade.KindConflict, "ci: merge-on-green requires a wait result that resolved green")
	}
	if strings.TrimSpace(wait.HeadSHA) == "" {
		return cascade.New(cascade.KindConflict,
			"ci: merge-on-green requires the head SHA the wait resolved for; without it a moved ref cannot be detected")
	}
	if wait.Ref != opts.Ref || wait.Owner != opts.Owner || wait.Repo != opts.Repo {
		return cascade.Newf(cascade.KindConflict,
			"ci: merge-on-green refuses a wait result for %s/%s@%s against a merge targeting %s/%s@%s",
			wait.Owner, wait.Repo, wait.Ref, opts.Owner, opts.Repo, opts.Ref)
	}
	return nil
}

// mergeEvalRequest builds the EvalRequest merge-on-green evaluates.
// CommandLess:true takes the rung from MergeOnGreenCapability's registered
// class rather than from shell-command parsing -- there is no shell command
// here, only a JSON-RPC tool call (R-14.211).
func mergeEvalRequest(opts MergeOptions) policy.EvalRequest {
	return policy.EvalRequest{
		Subject:     opts.Subject,
		Capability:  MergeOnGreenCapability,
		CommandLess: true,
		Attributes: map[string]string{
			"owner": opts.Owner, "repo": opts.Repo,
			"pr": strconv.Itoa(opts.PR), "merge_on_green": "true",
		},
		DataClass:        opts.dataClass(),
		LaneMaxDataClass: mergeDataCeiling,
		Summary:          "merge-on-green: " + opts.Owner + "/" + opts.Repo + " #" + strconv.Itoa(opts.PR),
	}
}
