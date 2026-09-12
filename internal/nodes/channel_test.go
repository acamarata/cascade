package nodes

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDeferSelfUpdate(t *testing.T) {
	if !DeferSelfUpdate("node-managed") {
		t.Fatal("node-managed channel must defer")
	}
	for _, ch := range []string{"script", "brew", "oci", "manual", "", "unknown"} {
		if DeferSelfUpdate(ch) {
			t.Fatalf("channel %q must not defer", ch)
		}
	}
}

func TestChannelDeferralError(t *testing.T) {
	err := ChannelDeferralError()
	if kind, _ := cascade.KindOf(err); kind != cascade.KindPolicyDenied {
		t.Fatalf("kind = %v, want KindPolicyDenied", kind)
	}
}
