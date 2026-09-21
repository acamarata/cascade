// Purpose (this file): wikiReply — the process-side dispatch for
//
//	"cascade-github.wiki.sync" and "cascade-github.wiki.check", routed
//	here by dispatch()'s wiki-prefix branch (main.go) because these
//	commands exec git (plugins/github/wiki) rather than call
//	tools.Client's REST API the way every other tool call does.
//
// Inputs: the RPC frame's params, decoded as wikiArgs.
// Outputs: the wiki package's own SyncResult/DriftResult as the RPC
//
//	result, or a typed error frame.
//
// Constraints: --yes and CASCADE_NO_INPUT are both resolved by
//
//	plugins/github/wiki itself (confirm.go): this process has no terminal
//	of its own, so args.Yes is whatever the RPC caller already obtained
//	(the CLI layer's --yes flag or interactive prompt, once mounted — see
//	main.go's doc comment on the still-unmounted CLI gap this shares with
//	T1's repos/issues/prs commands) and os.Getenv reads this process's own
//	environment for CASCADE_NO_INPUT, which ProcessRuntime's child process
//	inherits from the host by default.
//
// SPORT: plugins/github:wiki-cmd (ADD) — P1-E25-W5-S51-T6.
package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/tools"
	"github.com/acamarata/cascade/plugins/github/wiki"
)

// defaultWikiLocalDir is what an omitted local_dir argument resolves to:
// the repository's own wiki convention (ASI Policy 8 / this ticket's
// docs_updates).
const defaultWikiLocalDir = ".github/wiki"

// wikiArgs is the RPC params shape both wiki tools accept.
type wikiArgs struct {
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	LocalDir string `json:"local_dir"`
	Yes      bool   `json:"yes"`
}

// wikiReply answers one "cascade-github.wiki.*" call.
func (p *broker) wikiReply(in frame) (frame, bool) {
	var args wikiArgs
	if len(in.Params) > 0 {
		if err := json.Unmarshal(in.Params, &args); err != nil {
			return errorReply(in, err), true
		}
	}
	if args.LocalDir == "" {
		args.LocalDir = defaultWikiLocalDir
	}
	switch strings.TrimPrefix(in.Method, PluginName+".wiki.") {
	case "sync":
		return p.wikiSyncReply(in, args)
	case "check":
		return p.wikiCheckReply(in, args)
	default:
		return errorReply(in, cascade.Newf(cascade.KindNotFound, "cascade-github: no such tool %q", in.Method)), true
	}
}

// wikiSyncFunc is wiki.Sync, indirected through a var so a test can
// substitute a spy that records the SyncOptions this dispatch built —
// specifically opts.Token — without ever reaching a real git binary. The
// confirming review's own mutation (`Token: client.Token` -> `Token: ""`)
// proved no existing test could tell the two apart, because every
// existing test's fastest refusal path (confirmation, then git-absent)
// never depends on the token value; this seam is what makes that
// mutation observable. Production never assigns it.
var wikiSyncFunc = wiki.Sync

// wikiSyncReply implements "cascade-github.wiki.sync".
func (p *broker) wikiSyncReply(in frame, args wikiArgs) (frame, bool) {
	client := p.client()
	res, err := wikiSyncFunc(context.Background(), wiki.SyncOptions{
		Owner: args.Owner, Repo: args.Repo, LocalDir: args.LocalDir, Yes: args.Yes,
		Token:   client.Token,
		Checker: wikiVisibilityChecker{client: client},
		Getenv:  os.Getenv,
	})
	if err != nil {
		return errorReply(in, err), true
	}
	return frame{JSONRPC: "2.0", ID: in.ID, Result: res}, true
}

// wikiCheckResult is "cascade-github.wiki.check"'s RPC result: the
// drift report plus the exit code a CLI layer (once mounted) reports for
// it — computed once here, by wiki.DriftExitCode, rather than leaving
// every future caller to re-derive "non-zero on drift" from DriftResult's
// fields itself.
type wikiCheckResult struct {
	wiki.DriftResult
	ExitCode int `json:"exit_code"`
}

// wikiCheckReply implements "cascade-github.wiki.check". It never passes
// Yes or Getenv into wiki.CheckDrift because DriftOptions has neither
// field — the read-only path is prompt-free by construction, not by a
// runtime flag this dispatch would have to remember to omit.
func (p *broker) wikiCheckReply(in frame, args wikiArgs) (frame, bool) {
	client := p.client()
	res, err := wiki.CheckDrift(context.Background(), wiki.DriftOptions{
		Owner: args.Owner, Repo: args.Repo, LocalDir: args.LocalDir, Token: client.Token,
	})
	if err != nil {
		return errorReply(in, err), true
	}
	out := wikiCheckResult{DriftResult: res, ExitCode: wiki.DriftExitCode(res)}
	return frame{JSONRPC: "2.0", ID: in.ID, Result: out}, true
}

// wikiVisibilityChecker adapts the broker's existing tools.Client (the
// same transport repos.get/repos.clone_url already use) to
// wiki.VisibilityChecker, so plugins/github/wiki never has to know this
// plugin's HTTP transport exists.
type wikiVisibilityChecker struct {
	client tools.Client
}

// IsPrivate implements wiki.VisibilityChecker.
func (c wikiVisibilityChecker) IsPrivate(ctx context.Context, owner, repo string) (bool, error) {
	out, err := c.client.Call(ctx, "repos.get", tools.Args{Owner: owner, Repo: repo})
	if err != nil {
		return false, err
	}
	repoVal, ok := out.(tools.Repo)
	if !ok {
		return false, cascade.New(cascade.KindInternal, "cascade-github wiki: repos.get returned an unexpected type")
	}
	return repoVal.Private, nil
}
