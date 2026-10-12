package tierops

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestTierOperation(t *testing.T) {
	a, b := provider.SensitivityInternal, provider.SensitivityPublic
	_ = max(a, b) // want tier violation
}
