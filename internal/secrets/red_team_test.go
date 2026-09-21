// Package secrets_test (external, matching classes_test.go's identical
//
//	reasoning — see that file's doc comment on why this is not
//	`package secrets`).
//
// Purpose: the bridge-class red-team cases R-21.227 requires appended to
//
//	"internal/secrets/red_team_test.go" — adversarial attempts to push
//	local-only, restricted and unclassified content out of the process over
//	the Telegram bridge.
//
// WHAT CHANGED AND WHY. The first draft of this file called
//
//	Engine.InterceptClass directly, beside the code under test. That
//	ratifies a path production never takes: the bridge could have ignored
//	the class entirely and these tests would still have passed (the
//	adversarial review's finding #1). Every case below now drives the REAL
//	Telegram send path — telegram.BotClient.SendMessage over the REAL
//	production adapter (internal/plugins.NewBridgeEgressGate) over the REAL
//	default registry — and asserts on the bytes the transport was handed.
//	A bridge that stopped consulting the class, or stopped passing the tier,
//	or stopped posting the firewall's output, fails here.
//
// Constraints: no network. The transport is a Doer fake; the only thing
//
//	crossing a real boundary is the substitution pass over a temp-dir file
//	vault. No test here calls SelectCustody, so nothing can reach the
//	operator's keychain (R-14.206).
//
// SPORT: internal/secrets TestRedTeamBridgeClass*/ADDED
//
//	(P1-E23-W5-S48-T1).
package secrets_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/secrets"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// redTeamSecret is the value the red-team vault holds: no credential SHAPE at
// all, so only the exact-value substitution pass can catch it. That is the half
// a shape-only detector would miss and a bridge that bypassed the class would
// leak.
const redTeamSecret = "correct horse battery staple 4471"

// redTeamVault is a hermetic Vault holding exactly one value.
type redTeamVault struct{ name, value string }

func (v redTeamVault) List(context.Context) ([]string, error) { return []string{v.name}, nil }

func (v redTeamVault) Get(_ context.Context, name string) ([]byte, error) {
	if name != v.name {
		return nil, errors.New("red-team test vault: nothing stored under that name")
	}
	return []byte(v.value), nil
}

// recordingDoer captures what the bridge handed the transport. It is the only
// place these tests look for a leak: what a Telegram user would have received.
type recordingDoer struct {
	methods []string
	bodies  []string
}

// Do records the method and the rendered request body. The telegram package's
// params types are unexported, so the body is captured by formatting: what
// matters here is whether the secret's bytes are in what the transport was
// handed, not which struct field carried them.
func (d *recordingDoer) Do(_ context.Context, method string, params, _ any) error {
	d.methods = append(d.methods, method)
	d.bodies = append(d.bodies, fmt.Sprintf("%+v", params))
	return nil
}

// redTeamEngine builds the real engine over the one-value vault.
func redTeamEngine(t *testing.T) *egress.Engine {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("secrets.NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(),
		redTeamVault{name: "red_team_entry", value: redTeamSecret}, detector)
	if err != nil {
		t.Fatalf("egress.NewEngine: %v", err)
	}
	return engine
}

// redTeamBridge wires the REAL production egress adapter into a real BotClient
// whose transport is a recorder. This is the path a bound Telegram sender's
// reply actually takes.
func redTeamBridge(t *testing.T) (*telegram.BotClient, *recordingDoer) {
	t.Helper()
	gate := plugins.NewBridgeEgressGate(redTeamEngine(t))
	doer := &recordingDoer{}
	client := telegram.NewBotClient("tg-redteam", doer, gate,
		cascadepa.NewUpdateLedger(redTeamState{}))
	return client, doer
}

// redTeamState is a BridgeState that answers "nothing stored" — these cases
// never poll, so the ledger is never consulted for anything but its own
// construction.
type redTeamState struct{}

func (redTeamState) Load(context.Context, string) (cascadepa.SubjectState, bool, error) {
	return cascadepa.SubjectState{}, false, nil
}

func (redTeamState) Save(context.Context, cascadepa.SubjectState) error { return nil }

// TestRedTeamBridgeClass_RestrictedContentRefusedOnTheSendPath is the
// adversarial case: a reply the caller honestly classifies restricted must never
// leave, and the refusal must come from the CLASS, not from an upstream check
// somebody could forget.
func TestRedTeamBridgeClass_RestrictedContentRefusedOnTheSendPath(t *testing.T) {
	client, doer := redTeamBridge(t)
	err := client.SendMessage(context.Background(), 555000111,
		cascadepa.TierRestricted, "attempted restricted exfiltration")
	if err == nil {
		t.Fatal("restricted content was admitted onto the Telegram send path")
	}
	if !errors.Is(err, egress.ErrSensitivityViolation) {
		t.Fatalf("got %v, want egress.ErrSensitivityViolation", err)
	}
	if len(doer.methods) != 0 {
		t.Fatalf("the transport was reached for refused content: %v", doer.methods)
	}
}

// TestRedTeamBridgeClass_LocalOnlyContentRefusedOnTheSendPath mirrors the above
// for the strictest tier.
func TestRedTeamBridgeClass_LocalOnlyContentRefusedOnTheSendPath(t *testing.T) {
	client, doer := redTeamBridge(t)
	err := client.SendMessage(context.Background(), 555000111,
		cascadepa.TierLocalOnly, "attempted local-only exfiltration")
	if err == nil {
		t.Fatal("local-only content was admitted onto the Telegram send path")
	}
	if !errors.Is(err, egress.ErrSensitivityViolation) {
		t.Fatalf("got %v, want egress.ErrSensitivityViolation", err)
	}
	if len(doer.methods) != 0 {
		t.Fatalf("the transport was reached for refused content: %v", doer.methods)
	}
}

// TestRedTeamBridgeClass_UnclassifiedContentRefusedOnTheSendPath is §5.16's
// fail-closed default: a caller that forgot to classify gets the STRICT answer.
func TestRedTeamBridgeClass_UnclassifiedContentRefusedOnTheSendPath(t *testing.T) {
	client, doer := redTeamBridge(t)
	if err := client.SendMessage(context.Background(), 555000111,
		cascadepa.SensitivityTier(""), "unclassified content"); err == nil {
		t.Fatal("unclassified content was admitted onto the Telegram send path")
	}
	if len(doer.methods) != 0 {
		t.Fatalf("the transport was reached for unclassified content: %v", doer.methods)
	}
}

// TestRedTeamBridgeClass_AStoredSecretIsSubstitutedBeforeTheWire is the leak the
// review demonstrated: an admitted-tier reply carrying a stored vault value.
// The tier passes, so nothing refuses — the substitution pass is what has to
// catch it, and it only runs if the bridge hands the CONTENT to the class.
func TestRedTeamBridgeClass_AStoredSecretIsSubstitutedBeforeTheWire(t *testing.T) {
	client, doer := redTeamBridge(t)
	if err := client.SendMessage(context.Background(), 555000111,
		cascadepa.TierInternal, "here it is: "+redTeamSecret); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(doer.bodies) != 1 {
		t.Fatalf("the transport saw %d bodies, want 1", len(doer.bodies))
	}
	if strings.Contains(doer.bodies[0], redTeamSecret) {
		t.Fatalf("a stored vault value reached the Telegram transport verbatim: %q", doer.bodies[0])
	}
}

// TestRedTeamBridgeClass_AdmittedTiersActuallySend keeps the three refusals
// above from passing on a bridge that refuses everything.
func TestRedTeamBridgeClass_AdmittedTiersActuallySend(t *testing.T) {
	for _, tier := range []cascadepa.SensitivityTier{cascadepa.TierInternal, cascadepa.TierPublic} {
		client, doer := redTeamBridge(t)
		if err := client.SendMessage(context.Background(), 555000111, tier, "ordinary bridge content"); err != nil {
			t.Fatalf("tier %q was refused on the send path: %v", tier, err)
		}
		if len(doer.methods) != 1 || doer.methods[0] != telegram.MethodSendMessage {
			t.Fatalf("tier %q produced %v, want one sendMessage", tier, doer.methods)
		}
		if !strings.Contains(doer.bodies[0], "ordinary bridge content") {
			t.Fatalf("tier %q posted %q", tier, doer.bodies[0])
		}
	}
}

// TestRedTeamBridgeClass_CallbackAnswersCrossTheSameClass: answerCallbackQuery is
// an outbound path too, and the review found it ungated.
func TestRedTeamBridgeClass_CallbackAnswersCrossTheSameClass(t *testing.T) {
	client, doer := redTeamBridge(t)
	if err := client.AnswerCallbackQuery(context.Background(), "cb-1",
		cascadepa.TierRestricted, "refused"); err == nil {
		t.Fatal("a restricted callback answer was admitted")
	}
	if len(doer.methods) != 0 {
		t.Fatalf("the transport was reached: %v", doer.methods)
	}
	if err := client.AnswerCallbackQuery(context.Background(), "cb-2",
		cascadepa.TierInternal, "here it is: "+redTeamSecret); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if strings.Contains(doer.bodies[0], redTeamSecret) {
		t.Fatalf("a stored value reached the callback answer verbatim: %q", doer.bodies[0])
	}
}

// TestRedTeamBridgeClass_IsRegisteredStrictly keeps the class configuration
// itself pinned, so a widened registration fails here as well as in
// classes_test.go.
func TestRedTeamBridgeClass_IsRegisteredStrictly(t *testing.T) {
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassBridge)
	if !ok {
		t.Fatal("EgressClassBridge is not registered")
	}
	if cfg.AllowRestricted || cfg.AllowLocalOnly {
		t.Fatalf("the bridge class admits restricted or local-only content: %+v", cfg)
	}
}
