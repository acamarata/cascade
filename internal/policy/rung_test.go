package policy

// Purpose: pins the boundary R-14.211 created. Only a request that
//   DECLARES itself command-less takes its rung from the capability's
//   class. Text still classifies as text, and text the classifier does not
//   recognise is still L4, so the command-less path cannot be reached by
//   supplying a command the classifier happens not to know.
// Constraints: asserts against the spec's stated behaviour, not against
//   the implementation's own constants.

import (
	"context"
	"testing"
)

// TestUnclassifiableCommandStillDeniesEvenForALowClassCapability is the
// safety half of R-14.211. A low-class capability is exactly the case an
// attacker would want: if an unrecognised command could borrow the
// capability's rung, the classifier would be trivially bypassable by
// sending something it does not parse.
func TestUnclassifiableCommandStillDeniesEvenForALowClassCapability(t *testing.T) {
	e := layerFixture(t).engine
	capDef := Capability{Name: "test.low", Desc: "low", DefaultPolicy: ClassRead}

	got := e.resolveRung(context.Background(), EvalRequest{
		Capability: capDef.Name,
		// Not declared command-less: this IS a command, it is simply one
		// the classifier does not recognise.
		Action: "\x00\x01 not a parseable command \x00",
	}, capDef)

	if got != L4 {
		t.Fatalf("an unrecognised command resolved to %v; want L4, or the classifier is bypassable "+
			"by sending anything it cannot parse", got)
	}
}

// TestCommandLessRequestTakesTheCapabilityRung is the enabling half: a
// declared command-less action is judged by the capability, which is what
// lets a scheduled dispatch be authorized at all.
func TestCommandLessRequestTakesTheCapabilityRung(t *testing.T) {
	e := layerFixture(t).engine
	capDef := Capability{Name: "scheduler.dispatch", Desc: "cron", DefaultPolicy: ClassRead}

	got := e.resolveRung(context.Background(), EvalRequest{
		Capability:  capDef.Name,
		CommandLess: true,
	}, capDef)

	if want := capDef.Class().Risk(); got != want {
		t.Fatalf("a command-less request resolved to %v; want the capability's own rung %v", got, want)
	}
}

// TestEmptyActionWithoutTheDeclarationIsStillUnclassifiable pins the
// distinction the flag exists for. A hook whose command went missing is
// malformed, not command-less, and must not borrow the capability's rung.
func TestEmptyActionWithoutTheDeclarationIsStillUnclassifiable(t *testing.T) {
	e := layerFixture(t).engine
	capDef := Capability{Name: "hook.shell", Desc: "shell", DefaultPolicy: ClassRead}

	got := e.resolveRung(context.Background(), EvalRequest{
		Capability: capDef.Name,
		Action:     "",
	}, capDef)

	if got != L4 {
		t.Fatalf("an empty command with no command-less declaration resolved to %v; want L4", got)
	}
}
