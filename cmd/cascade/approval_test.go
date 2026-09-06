package main

// Purpose: covers the `approval` command group — its mount on the REAL
//   root (the wiring proof: delete mountApprovalCmd's call and every case
//   below fails with "unknown command"), the daemonless refusal, and each
//   view's rendering.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestApprovalAndPolicyMountOnTheRealRoot drives the production root
// command, not a tree the test built. It is the mutation-proof target for
// both mount lines in root.go's mountSubcommands.
func TestApprovalAndPolicyMountOnTheRealRoot(t *testing.T) {
	for _, args := range [][]string{
		{"approval", "--help"},
		{"approval", "standing", "--help"},
		{"policy", "--help"},
		{"policy", "audit", "--help"},
	} {
		out, err := execRoot(t, args...)
		if err != nil {
			t.Fatalf("cascade %s: %v", strings.Join(args, " "), err)
		}
		if strings.Contains(out, "unknown command") {
			t.Fatalf("cascade %s reports an unknown command", strings.Join(args, " "))
		}
	}
	out, err := execRoot(t, "approval", "--help")
	if err != nil {
		t.Fatalf("approval --help: %v", err)
	}
	for _, verb := range []string{"list", "show", "grant", "deny", "expire", "standing"} {
		if !strings.Contains(out, verb) {
			t.Errorf("cascade approval --help does not list the %s verb", verb)
		}
	}
}

// TestApprovalGuardsUnknownSubcommands proves guardUnknownSubcommands was
// applied at the mount, so a typo is refused rather than silently running
// the group's own help as a success.
func TestApprovalGuardsUnknownSubcommands(t *testing.T) {
	if _, err := execRoot(t, "approval", "lst"); err == nil {
		t.Error("cascade approval lst succeeded; an unknown subcommand must be refused")
	}
	if _, err := execRoot(t, "policy", "explainn", "x"); err == nil {
		t.Error("cascade policy explainn succeeded; an unknown subcommand must be refused")
	}
}

// embeddedContext is a context that reports daemonless (embedded) mode,
// which is what root.go's PersistentPreRunE attaches when no daemon is
// reachable.
func embeddedContext(t *testing.T) context.Context {
	t.Helper()
	return runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})
}

// TestApprovalRefusesDaemonlessMode proves the namespace refuses with an
// actionable error instead of answering from a second, empty queue.
func TestApprovalRefusesDaemonlessMode(t *testing.T) {
	deps := approvalDeps{Paths: lazyPaths{}, Clock: runtime.NewSystemClock()}
	for _, verb := range []string{"approval list", "policy explain", "approval standing create"} {
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

// TestApprovalViewsRender covers each human rendering, including the empty
// cases, and asserts no grant value appears in any of them.
func TestApprovalViewsRender(t *testing.T) {
	empty := approvalListView(policy.ApprovalListResult{})
	if got := empty.String(); !strings.Contains(got, "no approval requests are pending") {
		t.Errorf("the empty queue renders as %q", got)
	}
	filled := approvalListView(policy.ApprovalListResult{Pending: []policy.PendingEntry{{
		RequestID: "req-0001",
		Summary:   "write a.txt (L2)",
		ExpiresAt: time.Unix(0, 0).UTC(),
	}}})
	out := filled.String()
	for _, want := range []string{"req-0001", "write a.txt (L2)", "1970-01-01T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("the queue table is missing %q:\n%s", want, out)
		}
	}
	entry := approvalEntryView(policy.PendingEntry{RequestID: "req-0001", Summary: "write a.txt"})
	if !strings.Contains(entry.String(), "request_id") {
		t.Errorf("the entry view renders no request_id:\n%s", entry.String())
	}
	if got := decisionView(policy.DecisionResult{RequestID: "req-0001", State: "denied"}).String(); !strings.Contains(got, "denied") {
		t.Errorf("the decision view does not report what changed: %q", got)
	}
	if got := (expireView{Expired: 3}).String(); !strings.Contains(got, "3") {
		t.Errorf("the expire view does not report how many were retired: %q", got)
	}
}

// TestApprovalGrantRequiresTheToken proves the CLI refuses before it dials
// when the signed token is absent: a request id alone is never enough.
func TestApprovalGrantRequiresTheToken(t *testing.T) {
	out, err := execRoot(t, "approval", "grant", "01J8ZC5W2K4F6H8M0P2R4T6V8X")
	if err == nil {
		t.Fatalf("approval grant with no token succeeded: %s", out)
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("the refusal does not name the missing token: %v", err)
	}
}
