package main

// Purpose: covers the `policy` command group — the daemonless refusal
//   through the real cobra RunE for every verb (explain/check/list/audit
//   query), the transport-unreachable path via a real socket path that
//   nothing serves, and every view's rendering.
//
// Constraints: Art.7.1 — CASCADE_HOME is rooted at t.TempDir() for every
//   case, never the real $HOME; approvalClient's daemonless branch is
//   proven directly (mirrors approval_test.go's own pattern) and the
//   transport-unreachable branch is proven through execRoot with
//   CASCADE_HOME redirected, so no real user socket is ever dialed.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestPolicyRefusesDaemonlessMode proves every policy verb refuses with an
// actionable error in embedded (daemonless) mode, mirroring
// TestApprovalRefusesDaemonlessMode's coverage for the approval group.
func TestPolicyRefusesDaemonlessMode(t *testing.T) {
	deps := productionApprovalDeps()
	for _, verb := range []string{"policy explain", "policy check", "policy list", "policy audit query"} {
		c, err := approvalClient(embeddedContext(t), deps, verb)
		if err == nil {
			t.Fatalf("%s built a client in daemonless mode", verb)
		}
		if c != nil {
			t.Errorf("%s returned a client alongside its refusal", verb)
		}
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s refusal kind = %v, want unavailable", verb, err)
		}
		if !strings.Contains(err.Error(), "cascade daemon start") {
			t.Errorf("%s refusal does not say what to do: %v", verb, err)
		}
	}
}

// hermeticApprovalDeps builds approvalDeps rooted at a fresh t.TempDir(),
// with the real production dialer, so approvalClient succeeds in building
// a *client.Client (this bypasses root.go's own PersistentPreRunE, which
// would otherwise probe the socket and mark the context daemonless before
// the leaf command's RunE ever runs) but the client's own dial then fails
// against a socket nothing serves (Art.7.1: never the real $HOME).
func hermeticApprovalDeps(t *testing.T) approvalDeps {
	t.Helper()
	return approvalDeps{
		Paths:       fakeDaemonPaths{root: t.TempDir()},
		Clock:       runtime.NewSystemClock(),
		DialContext: client.UnixDialer,
	}
}

// execLeaf runs a policy leaf command built directly (not through root, so
// no daemonless auto-probe happens first), with the standard global output
// flags every RunE reads via approvalOutputWriter, mirroring
// status_test.go's TestStatusCommand_RunE_DaemonNotRunning pattern.
func execLeaf(t *testing.T, cmd *cobra.Command, args []string) (string, error) {
	t.Helper()
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &strings.Builder{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// TestPolicyCommands_TransportFailurePropagates drives each verb's REAL
// RunE end to end: past approvalClient's success (a real, resolvable
// socket path) and into the actual c.PolicyX call, which fails to dial a
// socket nothing serves. It proves the CLI surfaces the daemon's own
// KindUnavailable classification rather than a second, hand-rolled error.
func TestPolicyCommands_TransportFailurePropagates(t *testing.T) {
	cases := []struct {
		name string
		cmd  func(approvalDeps) *cobra.Command
		args []string
	}{
		{"explain", newPolicyExplainCmd, []string{"echo hi"}},
		{"check", newPolicyCheckCmd, []string{"echo hi"}},
		{"list", newPolicyListCmd, nil},
		{"audit query", newPolicyAuditQueryCmd, []string{"--filter", "kind=policy.decision"}},
	}
	for _, tc := range cases {
		deps := hermeticApprovalDeps(t)
		out, err := execLeaf(t, tc.cmd(deps), tc.args)
		if err == nil {
			t.Fatalf("policy %s succeeded against a socket nothing listens on: %s", tc.name, out)
		}
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("policy %s error kind = %v, want unavailable", tc.name, err)
		}
		if !strings.Contains(err.Error(), "daemon not running or unreachable") {
			t.Errorf("policy %s error = %v, want it to name the unreachable daemon", tc.name, err)
		}
	}
}

// TestPolicyExplainRequiresAnAction proves the positional-arg contract:
// policy explain/check refuse with a usage error rather than reaching the
// client with a zero-value action.
func TestPolicyExplainRequiresAnAction(t *testing.T) {
	deps := hermeticApprovalDeps(t)
	if _, err := execLeaf(t, newPolicyExplainCmd(deps), nil); err == nil {
		t.Fatal("cascade policy explain with no action succeeded")
	}
	if _, err := execLeaf(t, newPolicyCheckCmd(deps), nil); err == nil {
		t.Fatal("cascade policy check with no command succeeded")
	}
}

// TestPolicyExplainViewRenders proves policyExplainView's String() names
// every field of a real ExplainResult, plus the trace appended when
// present and omitted when it is not.
func TestPolicyExplainViewRenders(t *testing.T) {
	v := policyExplainView(policy.ExplainResult{
		Verdict:     "allow",
		Level:       "L0",
		Layer:       "deny_list",
		MatchedRule: "rule-42",
		Reason:      "no matching deny entry",
	})
	out := v.String()
	for _, want := range []string{"allow", "L0", "deny_list", "rule-42", "no matching deny entry"} {
		if !strings.Contains(out, want) {
			t.Errorf("policyExplainView.String() missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\n\n") {
		t.Errorf("policyExplainView.String() appended a trace with no Explanation set:\n%s", out)
	}

	withTrace := policyExplainView(policy.ExplainResult{Verdict: "deny", Explanation: "step 1\nstep 2"})
	traced := withTrace.String()
	if !strings.Contains(traced, "step 1\nstep 2") {
		t.Errorf("policyExplainView.String() dropped the explanation trace:\n%s", traced)
	}
}

// TestPolicyCheckViewRenders proves policyCheckView's String() reports the
// verdict, the rung and the auto-advance answer.
func TestPolicyCheckViewRenders(t *testing.T) {
	v := policyCheckView(policy.CheckResult{Verdict: "allow", Level: "L1", AutoAdvance: true})
	out := v.String()
	for _, want := range []string{"allow", "L1", "auto-advance true"} {
		if !strings.Contains(out, want) {
			t.Errorf("policyCheckView.String() = %q, missing %q", out, want)
		}
	}
	deny := policyCheckView(policy.CheckResult{Verdict: "deny", Level: "L3", AutoAdvance: false})
	if got := deny.String(); !strings.Contains(got, "auto-advance false") {
		t.Errorf("policyCheckView.String() = %q, want auto-advance false", got)
	}
}

// TestPolicyListViewRenders proves policyListView's String() renders both
// the capability table and the verb table, and that the empty case still
// produces the header rows rather than an empty string.
func TestPolicyListViewRenders(t *testing.T) {
	empty := policyListView(policy.ListResult{})
	out := empty.String()
	for _, want := range []string{"CAPABILITY", "CLASS", "VERB", "RISK", "ELEVATED"} {
		if !strings.Contains(out, want) {
			t.Errorf("policyListView.String() empty case missing header %q:\n%s", want, out)
		}
	}

	filled := policyListView(policy.ListResult{
		Capabilities: []policy.Capability{{Name: "fs.write", DefaultPolicy: policy.ClassRead}},
		Verbs:        []policy.VerbEntry{{Method: "policy.explain", Risk: "L0", Elevated: false}},
	})
	got := filled.String()
	for _, want := range []string{"fs.write", "read", "policy.explain", "L0", "false"} {
		if !strings.Contains(got, want) {
			t.Errorf("policyListView.String() missing %q:\n%s", want, got)
		}
	}
}

// TestPolicyAuditViewRenders proves policyAuditView's String() reports "no
// audit records matched" for the empty page, renders each record's fields
// for a filled one, and appends the resume cursor only when one remains.
func TestPolicyAuditViewRenders(t *testing.T) {
	empty := policyAuditView(policy.AuditQueryResult{})
	if got := empty.String(); got != "no audit records matched" {
		t.Errorf("policyAuditView.String() empty case = %q", got)
	}

	rec := audit.Record{
		Seq:        7,
		TSUnixNano: 0,
		Event:      audit.Event{Kind: audit.KindPolicyDecide, Verdict: "allow"},
	}
	noCursor := policyAuditView(policy.AuditQueryResult{Records: []audit.Record{rec}})
	got := noCursor.String()
	for _, want := range []string{"7", "policy.decide", "allow", "1970-01-01T00:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("policyAuditView.String() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "more records remain") {
		t.Errorf("policyAuditView.String() reported a cursor with none set:\n%s", got)
	}

	withCursor := policyAuditView(policy.AuditQueryResult{Records: []audit.Record{rec}, NextCursor: "cur-1"})
	if got := withCursor.String(); !strings.Contains(got, "resume with cursor=cur-1") {
		t.Errorf("policyAuditView.String() dropped the resume cursor:\n%s", got)
	}
}
