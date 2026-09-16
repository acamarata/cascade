package nodes

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestErrNoEligibleNodeIsTypedUnavailable pins the kind. The request was
// well formed and permitted; there is simply nowhere to run it. A caller
// distinguishing "retry later" from "this will never work" reads the kind,
// not the message.
func TestErrNoEligibleNodeIsTypedUnavailable(t *testing.T) {
	err := ErrNoEligibleNode(Requirement{}, []Exclusion{{NodeID: "n1", Reason: ReasonDrained, Detail: "drained"}})
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindUnavailable)
	}
}

// TestErrNoEligibleNodeDistinguishesAnEmptyFleet proves "there are no nodes"
// reads differently from "there are nodes and none qualified" — the first is
// an enrollment problem, the second a fleet-state problem.
func TestErrNoEligibleNodeDistinguishesAnEmptyFleet(t *testing.T) {
	empty := ErrNoEligibleNode(Requirement{}, nil).Error()
	if !strings.Contains(empty, "no enrolled nodes") {
		t.Fatalf("empty-fleet error = %q, want it to say the fleet is empty", empty)
	}

	populated := ErrNoEligibleNode(Requirement{}, []Exclusion{
		{NodeID: "n1", Reason: ReasonDrained, Detail: "drained"},
	}).Error()
	if strings.Contains(populated, "no enrolled nodes") {
		t.Fatalf("populated-fleet error = %q, want it to report the considered nodes", populated)
	}
	if !strings.Contains(populated, "1 considered") {
		t.Fatalf("populated-fleet error = %q, want it to count what was considered", populated)
	}
}

// TestErrNoEligibleNodeNamesRequiredCapabilities proves the requirement is
// echoed back. Without it an operator sees that nothing matched but not
// what was being asked for.
func TestErrNoEligibleNodeNamesRequiredCapabilities(t *testing.T) {
	err := ErrNoEligibleNode(
		Requirement{Capabilities: []string{"browser", "docker"}},
		[]Exclusion{{NodeID: "n1", Reason: ReasonMissingCapability, Detail: "docker"}},
	).Error()
	for _, want := range []string{"browser", "docker"} {
		if !strings.Contains(err, want) {
			t.Errorf("error = %q, want it to name the required capability %q", err, want)
		}
	}
}

// TestErrNoEligibleNodeSummarizesByReason is the aggregate an operator
// actually needs: whether this is one broken node or a fleet-wide
// condition. The counts must be right and the order stable.
func TestErrNoEligibleNodeSummarizesByReason(t *testing.T) {
	err := ErrNoEligibleNode(Requirement{}, []Exclusion{
		{NodeID: "a", Reason: ReasonDrained, Detail: "drained"},
		{NodeID: "b", Reason: ReasonNotReachable, Detail: "unknown"},
		{NodeID: "c", Reason: ReasonDrained, Detail: "drained"},
		{NodeID: "d", Reason: ReasonDrained, Detail: "drained"},
		{NodeID: "e", Reason: ReasonNotReachable, Detail: "unknown"},
	}).Error()

	if !strings.Contains(err, "3 drained") {
		t.Errorf("error = %q, want it to count 3 drained", err)
	}
	if !strings.Contains(err, "2 not-reachable") {
		t.Errorf("error = %q, want it to count 2 not-reachable", err)
	}
	// Most common first, so the dominant condition leads.
	if strings.Index(err, "3 drained") > strings.Index(err, "2 not-reachable") {
		t.Errorf("error = %q, want the most common reason first", err)
	}
}

// TestReasonSummaryIsStableForEqualCounts proves ties break
// alphabetically rather than by map iteration order, so the same fleet
// state always produces the same message.
func TestReasonSummaryIsStableForEqualCounts(t *testing.T) {
	exclusions := []Exclusion{
		{NodeID: "a", Reason: ReasonNotReachable},
		{NodeID: "b", Reason: ReasonDrained},
		{NodeID: "c", Reason: ReasonNotConnected},
	}
	first := summarizeReasons(exclusions)
	for i := 0; i < 20; i++ {
		if got := summarizeReasons(exclusions); got != first {
			t.Fatalf("summary changed between runs: %q then %q", first, got)
		}
	}
	if !strings.HasPrefix(first, "1 drained") {
		t.Fatalf("summary = %q, want equal counts ordered alphabetically", first)
	}
}

// TestPerNodeDetailIsBoundedAndSaysSo proves a large fleet produces a
// readable error, and that the truncation is stated rather than silent — an
// operator must not think five nodes were considered when fifty were.
func TestPerNodeDetailIsBoundedAndSaysSo(t *testing.T) {
	var exclusions []Exclusion
	for i := 0; i < maxPerNodeDetail+7; i++ {
		exclusions = append(exclusions, Exclusion{NodeID: "node", Reason: ReasonDrained, Detail: "drained"})
	}
	err := ErrNoEligibleNode(Requirement{}, exclusions).Error()

	if !strings.Contains(err, "and 7 more") {
		t.Errorf("error = %q, want it to state how many nodes were omitted", err)
	}
	if !strings.Contains(err, "12 considered") {
		t.Errorf("error = %q, want the full count even though detail was truncated", err)
	}
}

// TestPerNodeDetailNamesEachNode proves the detail identifies which node
// failed for which reason when the fleet is small enough to list.
func TestPerNodeDetailNamesEachNode(t *testing.T) {
	err := ErrNoEligibleNode(Requirement{}, []Exclusion{
		{NodeID: "alpha", Reason: ReasonDrained, Detail: "node is drained"},
		{NodeID: "beta", Reason: ReasonNotConnected, Detail: "tunnel is down"},
	}).Error()
	for _, want := range []string{"alpha", "node is drained", "beta", "tunnel is down"} {
		if !strings.Contains(err, want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}
