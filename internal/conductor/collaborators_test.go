package conductor

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): the four production security-pipeline
//   collaborators' contracts — what each one refuses, and the one that
//   deliberately does not.
// Constraints: pure; no clock, no I/O.
// SPORT: conductor.collaborators tests (ADD) — P1-E10-W4-S87-T1.

// fakeScanner reports the classes it was told to find.
type fakeScanner struct{ classes []string }

func (f fakeScanner) ScanCertainClasses(content string) []string {
	if strings.Contains(content, "sk-live") {
		return f.classes
	}
	return nil
}

// requestWith builds a request carrying body as its single turn.
func requestWith(body string) provider.ModelRequest {
	return provider.ModelRequest{
		TaskID:    "P1-T1",
		TaskClass: "code",
		Inputs:    []provider.ChatMessage{{Role: "user", Content: body}},
	}
}

// TestAClassifierWithNoScannerIsRefused holds the fail-closed constructor.
// A classifier that inspects nothing passes every request, which reads to
// an operator exactly like one that works.
func TestAClassifierWithNoScannerIsRefused(t *testing.T) {
	if _, err := NewContentClassifier(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestCredentialMaterialNeverReachesTheDoor is the classifier's whole job.
func TestCredentialMaterialNeverReachesTheDoor(t *testing.T) {
	c, err := NewContentClassifier(fakeScanner{classes: []string{"anthropic-api-key"}})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Classify(context.Background(), requestWith("here is my key sk-live-abc"))
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied", err)
	}
	if !strings.Contains(err.Error(), "anthropic-api-key") {
		t.Errorf("err = %v, want the class named", err)
	}
	// The message must not carry the material itself: it reaches logs and
	// the RPC caller, and a precise message is a second disclosure.
	if strings.Contains(err.Error(), "sk-live") {
		t.Errorf("the refusal quotes the credential back: %v", err)
	}
	if err := c.Classify(context.Background(), requestWith("nothing secret here")); err != nil {
		t.Errorf("clean content was refused: %v", err)
	}
}

// TestTheTaxonomyRegistryReportsTheFrozenNine keeps the collaborator in
// step with the table rather than carrying a second copy of it.
func TestTheTaxonomyRegistryReportsTheFrozenNine(t *testing.T) {
	got := TaskClassRegistry{}.Classes()
	if len(got) != 9 {
		t.Fatalf("Classes() = %v (%d), want the frozen nine", got, len(got))
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c] {
			t.Errorf("class %q appears twice", c)
		}
		seen[c] = true
	}
	for _, want := range []string{"classify", "segment", "summarize", "extract", "chat",
		"code", "reason", "review", "arbitrate"} {
		if !seen[want] {
			t.Errorf("Classes() is missing %q", want)
		}
	}
}

// TestSensitivityOnlyEverNarrows is the property the whole tier system
// rests on.
func TestSensitivityOnlyEverNarrows(t *testing.T) {
	gate := FailClosedSensitivity{}
	var invalid provider.SensitivityTier = 99
	if got := gate.Resolve(invalid); got != provider.SensitivityRestricted {
		t.Errorf("an undeclared tier resolved to %v, want restricted", got)
	}
	for _, tier := range []provider.SensitivityTier{
		provider.SensitivityRestricted, provider.SensitivityLocalOnly,
	} {
		if got := gate.Resolve(tier); got != tier {
			t.Errorf("Resolve(%v) = %v, want it unchanged", tier, got)
		}
	}
}

// denyAll refuses every task class.
type denyAll struct{ reason string }

func (d denyAll) DeniedTaskClass(context.Context, string) (string, bool) { return d.reason, true }

// TestTheOwnersOwnDeniesAreEnforced covers the policy evaluator's actual
// job, and the deliberate default beside it.
func TestTheOwnersOwnDeniesAreEnforced(t *testing.T) {
	// With no store configured, the operator's own request is allowed:
	// identity was proven below this seam by the socket-owner check, and
	// refusing here would only mean the tool never runs.
	if err := NewOwnerPolicy(nil).Authorize(context.Background(), requestWith("x")); err != nil {
		t.Fatalf("a request was refused with no deny rules configured: %v", err)
	}
	err := NewOwnerPolicy(denyAll{reason: "no external code dispatch"}).
		Authorize(context.Background(), requestWith("x"))
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied", err)
	}
	for _, want := range []string{"code", "no external code dispatch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}
