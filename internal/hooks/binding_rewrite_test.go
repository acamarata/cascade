// Purpose: BoundRehydrate refuses a rehydrated object a vault value
//
//	reshaped. The params go to the rehydrator as one JSON object, so a
//	value holding JSON syntax could add, repeat or rewrite a param (the
//	plugin id among them); the seam refuses that, quoting nothing. A
//	malformed tag refuses with a fixed text, never its body.
//
// Inputs: the boundRig of binding_test.go; generated fake values only.
package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBoundRehydrateRefusesValueRewritingParams(t *testing.T) {
	rig := newBoundRig(t, map[string]string{"VAULT_X": `x","zz":"p9`, "VAULT_Q": `x","plugin":"p9`, "VAULT_M": `x","mode":"p9`})
	seam := BoundRehydrate(admittedOf(VaultBinding{"h", "p1", "VAULT_X"}, VaultBinding{"h", "p1", "VAULT_Q"},
		VaultBinding{"h", "p1", "VAULT_M"}), rig.bytes)
	for _, tagged := range []map[string]string{
		{"plugin": "p1", "a": "<apikey>VAULT_X</apikey>"},
		{"plugin": "p1", "z": "<apikey>VAULT_Q</apikey>"},
		{"plugin": "p1", "hdr": "<apikey>VAULT_M</apikey>", "mode": "a<b"}, // an untagged param holding '<'
	} {
		plain, _, err := seam(context.Background(), pluginFire("h"), tagged)
		if plain != nil || !cascade.HasKind(err, cascade.KindIntegrity) || strings.Contains(err.Error(), "p9") {
			t.Fatalf("%v: plain=%v err=%v, want an integrity refusal quoting nothing", tagged, plain, err)
		}
	}
}

func TestBoundRehydrateMalformedTagQuotesNothing(t *testing.T) {
	rig := newBoundRig(t, nil)
	tagged := map[string]string{"plugin": "p1", "a": "<apikey>pasted-fake-value</apikey>"}
	plain, _, err := BoundRehydrate(admittedOf(), rig.bytes)(context.Background(), pluginFire("h"), tagged)
	if plain != nil || err == nil || strings.Contains(err.Error(), "pasted") || !cascade.HasKind(err, cascade.KindInvalidInput) ||
		err.Error() != "invalid-input: hooks: the tagged action parameters could not be rehydrated" || len(rig.vault.reads) != 0 {
		t.Fatalf("malformed tag: plain=%v err=%v, want a fixed-text refusal", plain, err)
	}
}
