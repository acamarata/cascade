package intake

// Purpose: the PINNED-kind half of the shape probe (R-14.264) — a caller
//   who names a driver kind gets that kind tried and nothing else, and a
//   failure that names the pin rather than blaming the credential.
// Constraints: in-memory Doer, no network (Art.7.2).
// SPORT: providers/intake pinned-shape probe (TEST) — P1-E16-W4-S35-T13.

import (
	"context"
	"strings"
	"testing"
)

// TestAPinnedKindTriesThatKindAndNothingElse is the assertion the pin
// exists for. The probe order would have reached anthropic first; the pin
// says openai-compat, so anthropic is never dialled.
func TestAPinnedKindTriesThatKindAndNothingElse(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 200, Body: loadFixture(t, "probe_anthropic.golden.json")},
		"https://api.openai.com/v1/models":    {Status: 200, Body: loadFixture(t, "probe_openai_compat.golden.json")},
	}}
	kind, _, _, err := shapeProbeFor(context.Background(), doer, engine, "sk-test", "", DriverOpenAICompat)
	if err != nil {
		t.Fatalf("shapeProbeFor: %v", err)
	}
	if kind != DriverOpenAICompat {
		t.Fatalf("kind = %s, want the pinned %s; the probe overruled somebody who said", kind, DriverOpenAICompat)
	}
	if len(doer.calls) != 1 {
		t.Fatalf("%d calls, want exactly 1: a pinned kind must not dial the others", len(doer.calls))
	}
}

// TestAPinnedKindThatFailsNamesThePin pins R-14.264's actual complaint. An
// author who pinned a shape and got "none of anthropic-compat,
// openai-compat or gemini matched this credential" goes and checks their
// credential, which is not the problem.
func TestAPinnedKindThatFailsNamesThePin(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.openai.com/v1/models": {Status: 404},
	}}
	_, _, _, err := shapeProbeFor(context.Background(), doer, engine, "sk-test", "", DriverOpenAICompat)
	if err == nil {
		t.Fatal("a pinned kind the endpoint does not speak must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, string(DriverOpenAICompat)) {
		t.Errorf("error does not name the pinned kind: %q", msg)
	}
	if !strings.Contains(msg, "404") {
		t.Errorf("error does not carry what the endpoint answered: %q", msg)
	}
	if strings.Contains(msg, string(DriverAnthropic)) {
		t.Errorf("error blames a shape that was never tried: %q", msg)
	}
}

// TestAPinnedKindWithNoProbeTargetIsRefused covers the kinds this build
// declares but cannot verify over the wire. Quietly probing the other
// three would register a provider under a driver nobody asked for.
func TestAPinnedKindWithNoProbeTargetIsRefused(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{}
	_, _, _, err := shapeProbeFor(context.Background(), doer, engine, "sk-test", "", DriverOllama)
	if err == nil {
		t.Fatal("a kind with no probe target must be refused, not probed as something else")
	}
	if len(doer.calls) != 0 {
		t.Errorf("%d call(s) made for an unprobeable kind: %+v", len(doer.calls), doer.calls)
	}
	msg := err.Error()
	if !strings.Contains(msg, string(DriverOllama)) {
		t.Errorf("error does not name the kind that was refused: %q", msg)
	}
	// The message has to say what WOULD work, or an author is left
	// guessing at a list they cannot see.
	for _, probeable := range []DriverKind{DriverAnthropic, DriverOpenAICompat, DriverGemini} {
		if !strings.Contains(msg, string(probeable)) {
			t.Errorf("error does not name the probeable kind %s: %q", probeable, msg)
		}
	}
}

// TestCandidatesForReturnsOneTargetOrNone is the unit under both branches
// above, asserted directly so a future kind added to probeOrder without a
// target is caught here rather than at an endpoint.
func TestCandidatesForReturnsOneTargetOrNone(t *testing.T) {
	if got := candidatesFor(DriverAnthropic); len(got) != 1 || got[0].kind != DriverAnthropic {
		t.Errorf("candidatesFor(anthropic) = %+v, want exactly the anthropic target", got)
	}
	if got := candidatesFor(DriverLocalLLM); got != nil {
		t.Errorf("candidatesFor(local-llm) = %+v, want nothing: this build has no probe target for it", got)
	}
}

// TestEveryDeclaredKindIsEitherProbeableOrRefused joins the two lists that
// must not drift. A kind DriverKinds() declares and probeOrder has no
// target for is legitimate — it is refused with a message — but a kind
// probeOrder carries that DriverKinds() omits would be unreachable from a
// setup file, which is the drift R-14.264 removed.
func TestEveryDeclaredKindIsEitherProbeableOrRefused(t *testing.T) {
	declared := map[DriverKind]bool{}
	for _, k := range DriverKinds() {
		declared[k] = true
	}
	for _, target := range probeOrder {
		if !declared[target.kind] {
			t.Errorf("probeOrder can probe %q, which DriverKinds() does not declare: "+
				"a setup file could never name it", target.kind)
		}
	}
	if len(DriverKinds()) == 0 {
		t.Fatal("DriverKinds() declares nothing; the setup-file parser would refuse every kind")
	}
}
