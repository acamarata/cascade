package main

// Purpose (this file): the generic-verb envelope edges: every flag the
//   host forwards lands on its tool argument, bad flag values and bad
//   positionals are typed invalid-input refusals, and an undecodable
//   envelope never reaches the transport.
// SPORT: plugins/github tests (ADD).

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/tools"
)

func TestCmdEnvelopeAppliesEveryFlag(t *testing.T) {
	tool, a, err := mapCmd("prs", cmdEnvelope{
		Args: []string{"create", "acamarata/qibla"},
		Flags: map[string]string{
			"state": "open", "title": "T", "body": "B", "head": "h", "base": "main",
			"merge-method": "squash", "label": "bug, ,ui", "reviewers": "a,b",
			"team-reviewers": "core", "draft": "true", "per-page": "5",
		},
	})
	if err != nil || tool != "prs.create" {
		t.Fatalf("mapCmd = %q, %v", tool, err)
	}
	want := tools.Args{
		Owner: "acamarata", Repo: "qibla", State: "open", Title: "T", Body: "B", Head: "h", Base: "main",
		MergeMethod: "squash", Labels: []string{"bug", "ui"}, Reviewers: []string{"a", "b"},
		TeamReviewers: []string{"core"}, Draft: true, PerPage: 5,
	}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("args = %+v, want %+v", a, want)
	}
}

func TestCmdEnvelopeMapsNumberedTool(t *testing.T) {
	tool, a, err := mapCmd("issues", cmdEnvelope{Args: []string{"close", "o/r", "42"}})
	if err != nil || tool != "issues.close" || a.Owner != "o" || a.Repo != "r" || a.Number != 42 {
		t.Fatalf("mapCmd = %q, %+v, %v; want issues.close on o/r #42", tool, a, err)
	}
}

// refusal is one envelope mapCmd must refuse with kind and a message part.
type refusal struct {
	noun string
	env  cmdEnvelope
	kind cascade.Kind
	text string
}

func TestCmdEnvelopeRefusesBadInput(t *testing.T) {
	cases := []refusal{
		{"gists", cmdEnvelope{Args: []string{"list"}}, cascade.KindUnsupported, "no generic verb"},
		{"repos", cmdEnvelope{}, cascade.KindInvalidInput, "sub-verb is required"},
		{"repos", cmdEnvelope{Args: []string{"delete", "o/r"}}, cascade.KindInvalidInput, "unknown sub-verb"},
		{"repos", cmdEnvelope{Args: []string{"list"}}, cascade.KindInvalidInput, "needs an owner"},
		{"repos", cmdEnvelope{Args: []string{"get", "noslash"}}, cascade.KindInvalidInput, "needs <owner>/<repo>"},
		{"prs", cmdEnvelope{Args: []string{"get", "o/r"}}, cascade.KindInvalidInput, "needs a number"},
		{"prs", cmdEnvelope{Args: []string{"get", "o/r", "x"}}, cascade.KindInvalidInput, "is not a number"},
		{"prs", flagEnv("draft", "maybe"), cascade.KindInvalidInput, "--draft"},
		{"prs", flagEnv("per-page", "many"), cascade.KindInvalidInput, "--per-page"},
		{"prs", flagEnv("force", "1"), cascade.KindInvalidInput, "unknown flag --force"},
	}
	for _, c := range cases {
		tool, _, err := mapCmd(c.noun, c.env)
		if tool != "" || !cascade.HasKind(err, c.kind) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("mapCmd(%s, %+v) = %q, %v; want %v naming %q", c.noun, c.env, tool, err, c.kind, c.text)
		}
	}
}

// flagEnv is a valid prs list envelope carrying one extra flag.
func flagEnv(name, value string) cmdEnvelope {
	return cmdEnvelope{Args: []string{"list", "o/r"}, Flags: map[string]string{name: value}}
}

func TestCmdReplyRefusesUndecodableEnvelope(t *testing.T) {
	doer := &fakeDoer{}
	p, _ := newTestPlugin(doer, nil, nil)
	id := uint64(9)
	for _, params := range []string{`[]`, `{"args":"list"}`} {
		out, ok := p.dispatch(frame{JSONRPC: "2.0", ID: &id, Method: cmdPrefix + "repos", Params: json.RawMessage(params)})
		if !ok || out.Error == nil || out.Result != nil {
			t.Fatalf("params %s: reply = %+v, want an error frame", params, out)
		}
	}
	if len(doer.calls) != 0 {
		t.Fatalf("%d requests reached the transport, want 0", len(doer.calls))
	}
}

func TestSplitListDropsBlanks(t *testing.T) {
	if got := splitList(" , ,"); got != nil {
		t.Fatalf("splitList(blanks) = %q, want nil", got)
	}
}
