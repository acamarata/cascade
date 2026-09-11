package conductor

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRouterSentinels_WrapFrozenKinds asserts the two router-local
// sentinels each wrap a member of the frozen 14-kind taxonomy.
func TestRouterSentinels_WrapFrozenKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind cascade.Kind
	}{
		{"ErrNoCapableProvider", ErrNoCapableProvider, cascade.KindCapabilityDenied},
		{"ErrAllProvidersEvicted", ErrAllProvidersEvicted, cascade.KindUnavailable},
	}
	for _, tc := range cases {
		if !cascade.HasKind(tc.err, tc.kind) {
			t.Errorf("%s does not wrap %s", tc.name, tc.kind)
		}
	}
}
