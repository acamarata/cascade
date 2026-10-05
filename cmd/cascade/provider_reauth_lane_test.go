//go:build !windows

// Purpose: proves a verified `provider reauth` clears the amber row a real
// 401 left. The lane is driven to auth-required by one call through the
// production resolver over the durable registry, then each reauth form runs
// against the same providers.db.
// Inputs: reauthDeps (provider_reauth_flagless_test.go: HOME, USERPROFILE
// and custody forced to a temp file vault through a recording keychain
// runner) and the resolver helpers in daemon_unix_conductor_lane_test.go.
// Outputs: none.
// Constraints: no network (fake Transport, fake intake Doer), no platform
// keychain (every test fails at cleanup if the runner was called), and the
// key values are assembled at run time.
// SPORT: cli.provider.reauth (CHANGE, P1-WID-11).

package main

import (
	"context"
	"strings"
	"testing"

	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// anthropic401 is a CONSTRUCTED anthropic authentication_error body (not a
// capture); the driver keys on the status, not on this text.
const anthropic401 = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`

// amberTheLane opens the durable registry, runs one call through the
// resolver with a 401 transport and returns the lane's stored state.
func amberTheLane(t *testing.T, deps providerDeps) providerregistry.LaneState {
	t.Helper()
	store, err := openProviderStorage(context.Background(), deps)
	if err != nil {
		t.Fatalf("openProviderStorage: %v", err)
	}
	defer func() { _ = store.Close() }()
	tr := &fixedTransport{status: 401, body: []byte(anthropic401)}
	cerr := callThroughResolver(t, store.Registry, runtime.NewSystemClock(), tr, "myclaude", "provider.myclaude.key")
	if !cascade.HasKind(cerr, cascade.KindPermissionDenied) || tr.calls != 1 {
		t.Fatalf("call = %v after %d sends, want the anthropic driver's KindPermissionDenied after one send", cerr, tr.calls)
	}
	state, _ := laneState(t, store.Registry, "myclaude")
	return state
}

// seededAmberDeps adds myclaude (verified, so its lane is available) and
// drives its lane to auth-required through the resolver.
func seededAmberDeps(t *testing.T) providerDeps {
	t.Helper()
	deps, _ := reauthDeps(t, map[string]string{"OLD_KEY": realisticKey("old"), "NEW_KEY": realisticKey("new")}, 200)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key-env", "OLD_KEY"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	if _, lanes := durableState(t, deps, "myclaude"); len(lanes) != 1 || lanes[0].State != providerregistry.LaneStateAvailable {
		t.Fatalf("seed lanes = %+v, want one available lane", lanes)
	}
	if got := amberTheLane(t, deps); got != providerregistry.LaneStateAuthRequired {
		t.Fatalf("lane = %q after a 401 through the resolver, want auth-required", got)
	}
	return deps
}

func TestReauthClearsAuthRequiredLane(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		failed bool
		want   providerregistry.LaneState
	}{
		{"verified reauth marks the lane available", []string{"--key-env", "NEW_KEY"}, false, providerregistry.LaneStateAvailable},
		{"no-verify marks the lane unknown", []string{"--key-env", "NEW_KEY", "--no-verify"}, false, providerregistry.LaneStateUnknown},
		{"a failing verify leaves the lane auth-required", []string{"--key-env", "NEW_KEY"}, true, providerregistry.LaneStateAuthRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := seededAmberDeps(t)
			if tc.failed {
				deps.Doer = anthropicFullDoer(401)
			}
			_, _, err := runProvider(t, deps, append([]string{"reauth", "myclaude"}, tc.args...)...)
			if tc.failed != (err != nil) {
				t.Fatalf("reauth err = %v, want failure=%v", err, tc.failed)
			}
			if err != nil && strings.Contains(err.Error(), "cred-value") {
				t.Fatalf("error text carries a credential: %v", err)
			}
			_, lanes := durableState(t, deps, "myclaude")
			if len(lanes) != 1 || lanes[0].State != tc.want {
				t.Fatalf("lanes after reauth = %+v, want one lane in state %q", lanes, tc.want)
			}
		})
	}
}
