// Purpose: unit coverage for dispatch_remote.go's
// newDispatchRemoteInterceptor: proves it builds a real
// egress.Engine-backed remote.Interceptor, and that it fails closed
// (refuses to build one at all) while EgressClassPluginRemote sits
// disabled on the process-wide default registry — the same registry
// production code reads (internal/hooks/egress/classes.go's
// defaultClasses table registers it Enabled: false, and nothing in this
// test binary flips that), so this is a real, not simulated, proof of
// the R-21.265 "disabled by default" requirement reaching all the way to
// the composition root.
//
// SPORT: internal/plugins dispatch-remote (ADD) — P1-E15-W4-S33-T4.
package plugins

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNewDispatchRemoteInterceptor_RefusesWhileClassDisabled(t *testing.T) {
	_, err := newDispatchRemoteInterceptor()
	if err == nil {
		t.Fatal("newDispatchRemoteInterceptor: want a refusal (EgressClassPluginRemote is disabled by default), got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
	}
}
