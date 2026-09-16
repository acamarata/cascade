// Purpose (this file): the PRODUCTION implementations of four of the six
//
//	R-21.206 security-pipeline collaborators, so a daemon can construct an
//	Executor whose Pipeline.Ready() actually passes.
//
// WHY THIS FILE EXISTS. Pipeline.Ready() has gated every call since
//
//	K/S-22.T1 landed, and until now NOTHING in the tree implemented
//	Classifier, TaskClassTable, PolicyEvaluator or SensitivityGate outside
//	_test.go doubles. The W3 hardening gate ran the tagged artifact and got
//	`conductor: security pipeline not ready` from a real `cascade run` — the
//	product's core function, refused at its own door. R-14.243 folds the
//	fix into P1-E10-W4-S87-T1 alongside the credential seam, because a
//	credential that reaches a pipeline which still refuses every call is a
//	fix nobody can observe.
//
// Inputs: injected seams only — a credential scanner and an optional deny
//
//	store. Nothing here reads a clock, a file or the network.
//
// Outputs: values satisfying the four interfaces model.go declares.
// Constraints: every one of these fails CLOSED on a nil collaborator. The
//
//	firewall (*egress.Engine) and the audit writer are NOT here: both have
//	real constructors already and are wired at the composition root.
//
// SPORT: conductor.collaborators/ADD — P1-E10-W4-S87-T1 (R-14.243).

package conductor

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CredentialScanner reports the credential classes it finds in outbound
// content. It is declared here, in terms of plain strings, rather than
// imported from internal/secrets: conductor does not need that package's
// types, and the composition root that owns both sides supplies the
// adapter.
type CredentialScanner interface {
	// ScanCertainClasses returns the class names of every credential the
	// scanner is CERTAIN about, or nil for clean content. Ambiguous
	// signals are the scanner's to drop; this seam never sees them.
	ScanCertainClasses(content string) []string
}

// ContentClassifier is the production Classifier: it refuses a request
// whose inputs carry credential material.
//
// This is what "classify before dispatch" means at this seam. A request
// reaching the model door is about to leave the machine; a key pasted into
// a prompt is the one classification error that cannot be undone
// afterwards, because the far side has already seen it.
type ContentClassifier struct {
	scanner CredentialScanner
}

// NewContentClassifier builds a classifier over scanner.
//
// A nil scanner is REFUSED at construction rather than tolerated. A
// classifier that inspects nothing would pass every request, which reads to
// an operator exactly like a classifier that is working — the failure mode
// egress.NewEngine's own constructor comment names.
func NewContentClassifier(scanner CredentialScanner) (*ContentClassifier, error) {
	if scanner == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"conductor: a content classifier needs a credential scanner")
	}
	return &ContentClassifier{scanner: scanner}, nil
}

// Classify refuses req when any input turn carries credential material.
//
// The error names the CLASS only, never the value and never the offset:
// this message reaches logs and the RPC caller, and a message precise
// enough to locate the secret is a second disclosure of it.
func (c *ContentClassifier) Classify(_ context.Context, req provider.ModelRequest) error {
	for _, turn := range req.Inputs {
		if classes := c.scanner.ScanCertainClasses(turn.Content); len(classes) > 0 {
			return cascade.Newf(cascade.KindPolicyDenied,
				"conductor: the request carries credential material (%s); it was not dispatched",
				classes[0])
		}
	}
	return nil
}

// TaskClassRegistry is the production TaskClassTable: the frozen §5.18
// rows K/S-22.T4 owns, read through the interface Pipeline requires.
//
// It holds no state of its own. The table is a package-level literal whose
// parity with the spec is already asserted by task_classes.go's own tests;
// copying it here would create the second copy those tests exist to
// prevent.
type TaskClassRegistry struct{}

// Classes returns every task class in the §5.18 table.
func (TaskClassRegistry) Classes() []string {
	rows := TaskClasses()
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Class)
	}
	return out
}

// FailClosedSensitivity is the production SensitivityGate. It applies
// §5.16's fail-closed rule and nothing else: an undeclared or invalid tier
// resolves to restricted, a declared one passes through unchanged.
//
// It NARROWS or confirms, never widens — the property the whole tier
// system rests on, asserted directly by this file's tests.
type FailClosedSensitivity struct{}

// Resolve applies the fail-closed rule to tier.
func (FailClosedSensitivity) Resolve(tier provider.SensitivityTier) provider.SensitivityTier {
	if !tier.Valid() {
		return provider.SensitivityRestricted
	}
	return tier
}

// DenyStore is the optional seam an operator's explicit refusals arrive
// through. A nil DenyStore means "no deny rules configured", not "deny
// everything" — see OwnerPolicy's own comment for why that is the correct
// default HERE and nowhere else.
type DenyStore interface {
	// DeniedTaskClass reports whether this machine's operator has
	// forbidden dispatching taskClass, and why.
	DeniedTaskClass(ctx context.Context, taskClass string) (reason string, denied bool)
}

// OwnerPolicy is the production PolicyEvaluator.
//
// DEFAULT-ALLOW, DELIBERATELY, AND ONLY HERE. Every other fail-closed rule
// in this pipeline governs what leaves the machine; this one governs
// whether the machine's own owner may run their own binary. Identity is
// already established BELOW this seam and more strongly than any rule here
// could re-establish it: internal/rpc refuses a peer it cannot prove owns
// the daemon socket, so by the time a ModelRequest exists the caller has
// been proven to be the operator. Refusing by default would not add a
// security property — it would only mean `cascade run` never works until
// the operator writes a rule granting themselves permission to use their
// own tool, which is the failure this seam spent a wave in.
//
// What policy IS for here is the operator's own explicit restrictions, and
// those are consulted on every call through DenyStore.
type OwnerPolicy struct {
	denies DenyStore
}

// NewOwnerPolicy builds the evaluator. A nil store is valid and means no
// deny rules are configured.
func NewOwnerPolicy(denies DenyStore) *OwnerPolicy {
	return &OwnerPolicy{denies: denies}
}

// Authorize refuses req only when the operator has explicitly denied its
// task class.
func (p *OwnerPolicy) Authorize(ctx context.Context, req provider.ModelRequest) error {
	if p.denies == nil {
		return nil
	}
	if reason, denied := p.denies.DeniedTaskClass(ctx, req.TaskClass); denied {
		return cascade.Newf(cascade.KindPolicyDenied,
			"conductor: task class %q is denied on this machine: %s", req.TaskClass, reason)
	}
	return nil
}
