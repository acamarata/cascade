package client

// Purpose: ModelFacing in the default no-network unit lane. The model-facing
//   transport runs on every request, so a dial that fails before any byte
//   moves still exercises it for both identity branches (process token set
//   and unset) without importing "net" or "net/http". The headers it sends
//   are asserted over a real socket in model_facing_test.go.
// SPORT: internal/client (ADD).

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestModelFacingKeepsTheTransportClassification(t *testing.T) {
	prev, had := os.LookupEnv(principal.EnvToken)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(principal.EnvToken, prev)
			return
		}
		_ = os.Unsetenv(principal.EnvToken)
	})
	for _, set := range []bool{false, true} {
		if set {
			t.Setenv(principal.EnvToken, "unit-token")
		} else if err := os.Unsetenv(principal.EnvToken); err != nil {
			t.Fatal(err)
		}
		c := New(missingSocket(t), UnixDialer, 2*time.Second).ModelFacing()
		err := c.Do(context.Background(), "status.get", nil, nil)
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("token set %v: err = %v, want KindUnavailable", set, err)
		}
	}
}
