// Purpose (this file): the generic-verb entry point. The host's generic
//
//	process verbs (`cascade github repos|issues|prs ...`) call
//	"cascade-github.cmd.<noun>" with the {"args", "flags"} envelope; this
//	file decodes it and maps it onto the tools this plugin already serves.
//
// Inputs: params {"args": [positional strings], "flags": {name: string}}.
// Outputs: the mapped tool's own decoded result, or a typed error reply.
// Constraints: argument validation stays in the tools package's request
//
//	builders, so a refused owner or number never reaches the network. This
//	file only splits positionals and converts flag strings; it adds no
//	egress path and no base-URL override.
//
// SPORT: plugins/github cmd envelope (ADD) — P1-PLG-04.
package main

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/tools"
)

// cmdPrefix is the method prefix the generic verbs call, routed before the
// tool prefix in dispatch.
const cmdPrefix = PluginName + ".cmd."

// cmdEnvelope is the contract:process-verb-envelope wire shape.
type cmdEnvelope struct {
	Args  []string          `json:"args"`
	Flags map[string]string `json:"flags"`
}

// cmdTools maps noun -> sub-verb -> tool name.
var cmdTools = map[string]map[string]string{
	"repos": {"list": "repos.list", "get": "repos.get", "clone-url": "repos.clone_url"},
	"issues": {
		"list": "issues.list", "get": "issues.get", "create": "issues.create",
		"comment": "issues.comment", "close": "issues.close",
	},
	"prs": {
		"list": "prs.list", "get": "prs.get", "diff": "prs.diff", "create": "prs.create",
		"merge": "prs.merge", "review-request": "prs.review_request",
	},
}

// numbered lists the tools whose second positional is an issue or PR number.
var numbered = map[string]bool{
	"issues.get": true, "issues.comment": true, "issues.close": true,
	"prs.get": true, "prs.diff": true, "prs.merge": true, "prs.review_request": true,
}

// cmdReply answers one "cascade-github.cmd.<noun>" call.
func (p *broker) cmdReply(in frame) (frame, bool) {
	var env cmdEnvelope
	if len(in.Params) > 0 {
		if err := json.Unmarshal(in.Params, &env); err != nil {
			return errorReply(in, cascade.Wrap(cascade.KindInvalidInput, err, "github: decoding the verb envelope")), true
		}
	}
	tool, args, err := mapCmd(strings.TrimPrefix(in.Method, cmdPrefix), env)
	if err != nil {
		return errorReply(in, err), true
	}
	result, err := p.client().Call(context.Background(), tool, args)
	if err != nil {
		return errorReply(in, err), true
	}
	return frame{JSONRPC: "2.0", ID: in.ID, Result: result}, true
}

// mapCmd turns one envelope into a tool name and its arguments.
func mapCmd(noun string, env cmdEnvelope) (string, tools.Args, error) {
	verbs, ok := cmdTools[noun]
	if !ok {
		return "", tools.Args{}, cascade.Newf(cascade.KindUnsupported, "github: no generic verb %q", noun)
	}
	if len(env.Args) == 0 {
		return "", tools.Args{}, cascade.Newf(cascade.KindInvalidInput,
			"github %s: a sub-verb is required (one of %s)", noun, strings.Join(sortedKeys(verbs), ", "))
	}
	tool, ok := verbs[env.Args[0]]
	if !ok {
		return "", tools.Args{}, cascade.Newf(cascade.KindInvalidInput,
			"github %s: unknown sub-verb %q (one of %s)", noun, env.Args[0], strings.Join(sortedKeys(verbs), ", "))
	}
	args, err := positionals(tool, env.Args[1:])
	if err != nil {
		return "", tools.Args{}, err
	}
	if err := applyFlags(&args, env.Flags); err != nil {
		return "", tools.Args{}, err
	}
	return tool, args, nil
}

// positionals reads the owner (repos.list) or owner/repo target and, for
// the numbered tools, the issue or pull request number.
func positionals(tool string, rest []string) (tools.Args, error) {
	var a tools.Args
	if len(rest) == 0 {
		return a, cascade.Newf(cascade.KindInvalidInput, "github: %s needs an owner argument", tool)
	}
	if tool == "repos.list" {
		a.Owner = rest[0]
		return a, nil
	}
	owner, repo, ok := strings.Cut(rest[0], "/")
	if !ok {
		return a, cascade.Newf(cascade.KindInvalidInput, "github: %s needs <owner>/<repo>, got %q", tool, rest[0])
	}
	a.Owner, a.Repo = owner, repo
	if !numbered[tool] {
		return a, nil
	}
	if len(rest) < 2 {
		return a, cascade.Newf(cascade.KindInvalidInput, "github: %s needs a number after <owner>/<repo>", tool)
	}
	n, err := strconv.Atoi(rest[1])
	if err != nil {
		return a, cascade.Newf(cascade.KindInvalidInput, "github: %s: %q is not a number", tool, rest[1])
	}
	a.Number = n
	return a, nil
}

// applyFlags copies the changed flags the envelope carries onto a.
func applyFlags(a *tools.Args, flags map[string]string) error {
	for name, v := range flags {
		switch name {
		case "state":
			a.State = v
		case "title":
			a.Title = v
		case "body":
			a.Body = v
		case "head":
			a.Head = v
		case "base":
			a.Base = v
		case "merge-method":
			a.MergeMethod = v
		case "label":
			a.Labels = splitList(v)
		case "reviewers":
			a.Reviewers = splitList(v)
		case "team-reviewers":
			a.TeamReviewers = splitList(v)
		case "draft":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return cascade.Newf(cascade.KindInvalidInput, "github: --draft: %q is not a boolean", v)
			}
			a.Draft = b
		case "per-page":
			n, err := strconv.Atoi(v)
			if err != nil {
				return cascade.Newf(cascade.KindInvalidInput, "github: --per-page: %q is not a number", v)
			}
			a.PerPage = n
		default:
			return cascade.Newf(cascade.KindInvalidInput, "github: unknown flag --%s", name)
		}
	}
	return nil
}

// splitList splits a comma-separated flag value, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sortedKeys lists m's keys in a stable order for messages.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
