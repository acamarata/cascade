//go:build !windows

// Purpose: a launched runtime's capability checker is exactly the host
// boundary factory's result for (id, EffectiveGrants(rec)); an unset or
// failing factory leaves it nil.
// SPORT: internal/plugins host boundary hook tests (P1-PLG-03).
package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeChecker is a distinguishable HostCapabilityChecker.
type fakeChecker struct{ name string }

func (fakeChecker) CheckHTTP(context.Context, string) error             { return nil }
func (fakeChecker) CheckStorage(context.Context, string) error          { return nil }
func (fakeChecker) CheckSecretRef(context.Context, string) (any, error) { return nil, nil }
func (fakeChecker) CheckGeneric(context.Context, string) error          { return nil }

func TestLaunchInstalledAssignsHostBoundary(t *testing.T) {
	f := newLaunchFix(t)
	f.installGitHub(t)
	f.grant(t, cascadeGitHubPluginID, githubManifest(t))
	rec, _, _ := LoadMetadata(context.Background(), f.store, cascadeGitHubPluginID)
	want := &fakeChecker{name: "boundary"}
	var gotID, gotGrants string
	factories := map[string]HostBoundaryFactory{
		"set": func(_ context.Context, env LaunchEnv, id string, grants []string) (process.HostCapabilityChecker, error) {
			gotID, gotGrants = id, strings.Join(grants, ",")
			if env.Store != f.store {
				t.Error("the factory was not handed the launch environment")
			}
			return want, nil
		},
		"failing": func(context.Context, LaunchEnv, string, []string) (process.HostCapabilityChecker, error) {
			return want, cascade.New(cascade.KindUnavailable, "boundary unavailable")
		},
		"unset": nil,
	}
	for name, factory := range factories {
		restore := SetHostBoundary(factory)
		h, err := LaunchInstalled(context.Background(), cascadeGitHubPluginID)
		restore()
		if err != nil {
			t.Fatalf("%s: LaunchInstalled: %v", name, err)
		}
		got := h.rt.CapabilityChecker
		_ = h.Close(context.Background())
		if name == "set" {
			if got != process.HostCapabilityChecker(want) || gotID != cascadeGitHubPluginID || gotGrants != strings.Join(EffectiveGrants(rec), ",") {
				t.Fatalf("set: checker %v for (%s, %s), want the factory's for (%s, %v)", got, gotID, gotGrants, cascadeGitHubPluginID, EffectiveGrants(rec))
			}
		} else if got != nil {
			t.Fatalf("%s factory: checker = %v, want nil", name, got)
		}
	}
}
