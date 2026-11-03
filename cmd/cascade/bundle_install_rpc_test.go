//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	bundleHashA = strings.Repeat("a", 64)
	bundleHashB = strings.Repeat("b", 64)
)

// isolateBundleHome points HOME, USERPROFILE and CASCADE_HOME at one fresh
// temp dir and returns it.
func isolateBundleHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home)
	return home
}

// requireBundleErr asserts err carries kind and exactly msg.
func requireBundleErr(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	got, ok := cascade.KindOf(err)
	if !ok || got != kind {
		t.Fatalf("error %v: kind %v, want %v", err, got, kind)
	}
	if err.Error() != kind.String()+": "+msg {
		t.Fatalf("error message %q, want %q", err.Error(), kind.String()+": "+msg)
	}
}

// newTestBundleService returns a service over a fresh dir and a fixed clock.
func newTestBundleService(t *testing.T, auditor bundleAuditor) (*bundleService, *runtime.FixedClock) {
	t.Helper()
	home := isolateBundleHome(t)
	clock := runtime.NewFixedClock(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	return newBundleService(filepath.Join(home, "bundles"), clock, auditor), clock
}

func bundleRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

const bundleNoWitnessMsg = "bundle.install needs a verified elevation for this call"

// TestBundleInstallRPCRequiresWitness: an unattested call and a witness for
// other params are refused KindElevationRequired and store nothing; an
// attested call over its own params stores one approval activate consumes.
func TestBundleInstallRPCRequiresWitness(t *testing.T) {
	if !rpc.IsElevatedVerb(bundleInstallMethod) {
		t.Fatal("bundle.install is not in the elevation table")
	}
	reg, call := newBundleElevatedRegistry(t)
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	params := bundleInstallParams{RepoID: "repo-1", Hash: bundleHashA, PolicyVersion: "v1"}

	_, eo := reg.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: bundleInstallMethod, Params: bundleRaw(t, params)})
	if eo == nil || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("unattested dispatch: %v", eo)
	}

	svc, _ := newTestBundleService(t, nil)
	_, err := svc.install(context.Background(), bundleRaw(t, params))
	requireBundleErr(t, err, cascade.KindElevationRequired, bundleNoWitnessMsg)

	other := bundleRaw(t, bundleInstallParams{RepoID: "repo-1", Hash: bundleHashB, PolicyVersion: "v1"})
	var swapped error
	reg.Register(bundleInstallMethod, func(ctx context.Context, _ json.RawMessage) (any, error) {
		if _, ok := rpc.VerifiedElevationFrom(ctx); !ok {
			t.Error("the swapped handler ran without a witness")
		}
		_, swapped = svc.install(ctx, other)
		return nil, swapped
	})
	if err := call(owner, bundleInstallMethod, params, nil); err == nil {
		t.Fatal("a witness for other params was accepted")
	}
	requireBundleErr(t, swapped, cascade.KindElevationRequired, bundleNoWitnessMsg)
	if len(svc.pending) != 0 {
		t.Fatalf("refused installs stored %d approvals", len(svc.pending))
	}
	checkAttestedBundleInstall(t, reg, call, svc, params)
}

// newBundleElevatedRegistry builds the daemon registry (without the
// completion gate) with an elevation that answers a real attestation.
func newBundleElevatedRegistry(t *testing.T) (*rpc.Registry, func(context.Context, string, any, any) error) {
	t.Helper()
	saved := daemonRegistrations
	daemonRegistrations = nil
	for _, r := range saved {
		if r.Name != "completion-gate" {
			daemonRegistrations = append(daemonRegistrations, r)
		}
	}
	t.Cleanup(func() { daemonRegistrations = saved })
	paths := fakeDaemonPaths{root: t.TempDir()}
	return newElevatedTestRegistry(t, paths.DataDir())
}

// checkAttestedBundleInstall: an attested install over its own params
// stores one approval that activate consumes.
func checkAttestedBundleInstall(t *testing.T, reg *rpc.Registry, call func(context.Context, string, any, any) error, svc *bundleService, params bundleInstallParams) {
	t.Helper()
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	reg.Register(bundleInstallMethod, svc.install)
	reg.Register(bundleActivateMethod, svc.activate)
	var reply bundleInstallReply
	if err := call(owner, bundleInstallMethod, params, &reply); err != nil {
		t.Fatalf("attested install: %v", err)
	}
	if reply.Hash != bundleHashA || reply.RepoID != "repo-1" {
		t.Fatalf("install reply %+v", reply)
	}
	var st bundleStatusReply
	if err := call(owner, bundleActivateMethod, bundleRefParams{RepoID: "repo-1", Hash: bundleHashA}, &st); err != nil {
		t.Fatalf("activate after attested install: %v", err)
	}
	if st.InstalledHash != bundleHashA || st.PolicyVersion != "v1" {
		t.Fatalf("activate reply %+v", st)
	}
}
