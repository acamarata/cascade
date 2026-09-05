// Purpose: the seven layers and their normative order. The ordering table
// is written from the ruling text (R-21.236, R-14.26) as a literal expected
// sequence, never derived from the code's own list, so a reordering of the
// stack fails here instead of it. The store is the REAL
// StoreGrants over a real SQLite file (Art.2).
//
// SPORT: internal/policy policy-engine/ADDED,
// data-class-layer-zero/ADDED (P1-E09-W2-S17-T2).
package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// commandForLevel returns a command the §5.15 table classifies at level.
// An invalid level yields input the shell grammar cannot parse.
func commandForLevel(level RiskLevel) string {
	switch level {
	case L0:
		return "ls"
	case L1:
		return "go test ./..."
	case L2:
		return "touch a.txt"
	case L3:
		return "git push"
	case L4:
		return "rm -rf /tmp/scratch"
	default:
		return "))(("
	}
}

// mutatingCap raises an action to L2 without reaching the deny rung.
func mutatingCap() Capability {
	return Capability{
		Name:          "workspace.write",
		Desc:          "write files in the workspace",
		DefaultPolicy: ClassWorkspaceMutation,
	}
}

// layerFixture is the engine fixture plus the mutating capability.
func layerFixture(t *testing.T) *engineFixture {
	t.Helper()
	f := newEngineFixture(t)
	if err := f.reg.Add(context.Background(), mutatingCap()); err != nil {
		t.Fatalf("registering the mutating capability: %v", err)
	}
	return f
}

// alwaysSameTurn authorizes everything, so a test can prove that total
// same-turn authorization still does not skip a later layer.
type alwaysSameTurn struct{}

func (alwaysSameTurn) Authorized(context.Context, Subject, string) (bool, error) {
	return true, nil
}

// refusingElevation is an elevation ledger with no matching nonce.
type refusingElevation struct{}

func (refusingElevation) Verify(context.Context, ElevationAttestation) error {
	return cascade.New(cascade.KindNotFound, "elevation: nonce not found")
}

// grantingElevation is a live attestation that verifies.
type grantingElevation struct{}

func (grantingElevation) Verify(context.Context, ElevationAttestation) error { return nil }

// TestSevenLayerOrder asserts the normative sequence and which layer
// decides each canonical case. The want lists are the ruling's order
// written out, not a read of ordered().
func TestSevenLayerOrder(t *testing.T) {
	ctx := context.Background()
	for _, tc := range sevenLayerCases() {
		f, req := tc.build(t)
		out, err := f.engine.Evaluate(ctx, req)
		if err != nil {
			t.Errorf("%s: Evaluate: %v", tc.name, err)
			continue
		}
		assertSequence(t, tc.name, out.Trace, tc.wantSeq)
		if out.Layer != tc.wantWho || out.Verdict != tc.wantVerd {
			t.Errorf("%s: decided at %s with %s, want %s with %s",
				tc.name, out.Layer, out.Verdict, tc.wantWho, tc.wantVerd)
		}
	}
}

// orderCase is one row of the ordering table.
type orderCase struct {
	name     string
	build    func(t *testing.T) (*engineFixture, EvalRequest)
	wantSeq  []DecisionLayer
	wantWho  DecisionLayer
	wantVerd Verdict
}

// sevenLayerCases is the table: one row per layer that can decide, each
// naming the layers that must have run before it. It is split in two only
// to stay under the function-length cap.
func sevenLayerCases() []orderCase {
	return append(earlyLayerCases(), lateLayerCases()...)
}

// earlyLayerCases covers the layers that can stop an evaluation before the
// capability and the profile are ever read.
func earlyLayerCases() []orderCase {
	ctx := context.Background()
	return []orderCase{
		{
			name: "layer 1 stops a deny-listed action before every later layer",
			build: func(t *testing.T) (*engineFixture, EvalRequest) {
				return layerFixture(t), readRequest(L4)
			},
			wantSeq:  []DecisionLayer{LayerDataClass, LayerDenyList},
			wantWho:  LayerDenyList,
			wantVerd: VerdictDeny,
		},
		{
			name: "layer 2 refuses an elevated verb with no attestation",
			build: func(t *testing.T) (*engineFixture, EvalRequest) {
				req := readRequest(L0)
				req.Verb = "vault.get"
				return layerFixture(t), req
			},
			wantSeq:  []DecisionLayer{LayerDataClass, LayerDenyList, LayerElevation},
			wantWho:  LayerElevation,
			wantVerd: VerdictDeny,
		},
		{
			name: "layer 3 overrides the capability default and the profile",
			build: func(t *testing.T) (*engineFixture, EvalRequest) {
				f := layerFixture(t)
				if err := f.store.Grant(ctx, validGrant()); err != nil {
					t.Fatalf("Grant: %v", err)
				}
				return f, readRequest(L0)
			},
			wantSeq: []DecisionLayer{
				LayerDataClass, LayerDenyList, LayerElevation, LayerStandingGrant,
			},
			wantWho:  LayerStandingGrant,
			wantVerd: VerdictAllow,
		},
	}
}

// lateLayerCases covers the layers that decide once nothing above matched.
func lateLayerCases() []orderCase {
	return []orderCase{
		{
			name: "layer 4 decides when the capability raised the level",
			build: func(t *testing.T) (*engineFixture, EvalRequest) {
				f := layerFixture(t)
				req := readRequest(L0)
				req.Capability = mutatingCap().Name
				return f, req
			},
			wantSeq: []DecisionLayer{
				LayerDataClass, LayerDenyList, LayerElevation,
				LayerStandingGrant, LayerCapabilityDefault,
			},
			wantWho:  LayerCapabilityDefault,
			wantVerd: VerdictAllow,
		},
		{
			name: "layer 5 applies only when nothing above matched",
			build: func(t *testing.T) (*engineFixture, EvalRequest) {
				return layerFixture(t), readRequest(L2)
			},
			wantSeq: []DecisionLayer{
				LayerDataClass, LayerDenyList, LayerElevation,
				LayerStandingGrant, LayerCapabilityDefault, LayerAutonomyProfile,
			},
			wantWho:  LayerAutonomyProfile,
			wantVerd: VerdictAllow,
		},
		{
			name: "layer 6 answers when no profile is loaded",
			build: func(t *testing.T) (*engineFixture, EvalRequest) {
				f := layerFixture(t)
				f.ctrl.profile.Store(nil)
				return f, readRequest(L2)
			},
			wantSeq: []DecisionLayer{
				LayerDataClass, LayerDenyList, LayerElevation, LayerStandingGrant,
				LayerCapabilityDefault, LayerAutonomyProfile, LayerFailClosed,
			},
			wantWho:  LayerFailClosed,
			wantVerd: VerdictDeny,
		},
	}
}

// assertSequence compares the layers a trace records against the expected
// order, and pins that the last layer, and only it, decided.
func assertSequence(t *testing.T, name string, tr Trace, want []DecisionLayer) {
	t.Helper()
	if len(tr.Layers) != len(want) {
		t.Errorf("%s: trace ran %d layers, want %d: %s", name, len(tr.Layers), len(want), tr.Explanation)
		return
	}
	for i, w := range want {
		got := tr.Layers[i]
		if got.Layer != w || got.Index != w.Index() {
			t.Errorf("%s: layer %d is %s (index %d), want %s (index %d)",
				name, i, got.Layer, got.Index, w, w.Index())
		}
		if decided := i == len(want)-1; got.Decided != decided {
			t.Errorf("%s: layer %s Decided = %v, want %v", name, got.Layer, got.Decided, decided)
		}
	}
	if tr.MatchedRule != want[len(want)-1].String() {
		t.Errorf("%s: MatchedRule = %q, want %q", name, tr.MatchedRule, want[len(want)-1])
	}
}

// TestDataClassLayerZeroUnconditional proves layer 0 is outside
// first-match-wins: a standing grant that would allow the action cannot
// reach the decision.
func TestDataClassLayerZeroUnconditional(t *testing.T) {
	ctx := context.Background()
	f := layerFixture(t)
	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	req := readRequest(L0)
	req.DataClass = DataClassSecret
	req.LaneMaxDataClass = DataClassPublic

	out, err := f.engine.Evaluate(ctx, req)
	if !errors.Is(err, ErrDataClassDenied) {
		t.Fatalf("error = %v, want ErrDataClassDenied", err)
	}
	if out.Verdict != VerdictDeny || out.Layer != LayerDataClass {
		t.Errorf("outcome = %+v, want a layer 0 deny", out)
	}
	if len(out.Trace.Layers) != 1 || out.Trace.Layers[0].Index != 0 {
		t.Errorf("a terminal layer 0 refusal ran %d layers: %s",
			len(out.Trace.Layers), out.Trace.Explanation)
	}
	// The same request under a lane that tolerates the class reaches the
	// grant, which is what proves the refusal above was layer 0's doing.
	req.LaneMaxDataClass = DataClassSecret
	if out, err = f.engine.Evaluate(ctx, req); err != nil || out.Layer != LayerStandingGrant {
		t.Errorf("under a tolerant lane the request decided at %s (%v), want the grant", out.Layer, err)
	}
}

// TestLayerThreeReadsBothGrantKinds covers layer 3 over the one GrantStore
// API: a plain grant and a conditional standing grant, each yielding its
// own verdict.
func TestLayerThreeReadsBothGrantKinds(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		grant Grant
		attrs map[string]string
		want  Verdict
	}{
		{"a plain grant yields allow", validGrant(), nil, VerdictAllow},
		{"a grant naming its own verdict yields it", Grant{
			Subject: testSubject(), Capability: readCap().Name,
			ScopeClass: corpus.VisibilityShared, Verdict: VerdictAsk,
		}, nil, VerdictAsk},
		{"a conditional standing grant is honoured", Grant{
			Subject: testSubject(), Capability: readCap().Name,
			ScopeClass: corpus.VisibilityShared,
			Conditions: map[string]string{"repo": "cascade"},
			Verdict:    VerdictAllow,
		}, map[string]string{"repo": "cascade"}, VerdictAllow},
	}
	for _, tc := range cases {
		f := layerFixture(t)
		if err := f.store.Grant(ctx, tc.grant); err != nil {
			t.Fatalf("%s: Grant: %v", tc.name, err)
		}
		req := readRequest(L0)
		req.Attributes = tc.attrs
		out, err := f.engine.Evaluate(ctx, req)
		if err != nil {
			t.Errorf("%s: Evaluate: %v", tc.name, err)
			continue
		}
		if out.Layer != LayerStandingGrant || out.Verdict != tc.want {
			t.Errorf("%s: decided %s at %s, want %s at the grant layer",
				tc.name, out.Verdict, out.Layer, tc.want)
		}
	}
}
