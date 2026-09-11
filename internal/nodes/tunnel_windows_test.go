//go:build windows

package nodes

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestTunnelServiceRefusedNativeWindows asserts RefuseTunnelServiceOnGOOS's
// refusal natively on a real windows/amd64 build (R-21.226), against
// runtime.GOOS itself rather than the literal string tunnel_test.go's
// cross-platform TestRefuseTunnelServiceOnGOOS already covers.
func TestTunnelServiceRefusedNativeWindows(t *testing.T) {
	err := RefuseTunnelServiceOnGOOS(tunnelServiceGOOS)
	if err == nil {
		t.Fatal("expected the controller-side tunnel service to refuse on windows")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnsupported {
		t.Fatalf("expected KindUnsupported, got %v (ok=%v)", k, ok)
	}
}
