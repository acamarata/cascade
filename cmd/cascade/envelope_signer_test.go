//go:build !windows

// Purpose: the installation identity key: provisioned once, never
//
//	regenerated over an unreadable or malformed entry, signing envelopes
//	that verify only under its own public key, and held in the TestMain
//	custody. The MA-2 helper (isolateInstallationKey) gives each test its
//	own file vault and clears both memos; nothing here reaches host custody.
//	No test here calls t.Parallel: they reset package memos and the selector.
//
// SPORT: cmd/cascade installation identity tests (P1-SEC-40).

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errTransientCustody is the injected custody read failure.
var errTransientCustody = cascade.New(cascade.KindUnavailable, "test: transient custody read failure")

// keyFaults scripts reads of the installation key and counts its creates.
type keyFaults struct {
	mu      sync.Mutex
	script  []func(stored []byte, err error) ([]byte, error)
	creates int
}

// arm queues one step for the next read of the installation key.
func (f *keyFaults) arm(step func([]byte, error) ([]byte, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, step)
}

// pending is how many armed steps no read has consumed; createCount is how
// many times the installation key was written (the Set fallback only).
func (f *keyFaults) pending() int     { f.mu.Lock(); defer f.mu.Unlock(); return len(f.script) }
func (f *keyFaults) createCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.creates }

// faultCustody wraps the helper's file vault.
type faultCustody struct {
	secrets.Custody
	f *keyFaults
}

func (c faultCustody) Get(ctx context.Context, name string) ([]byte, error) {
	v, err := c.Custody.Get(ctx, name)
	if name != secrets.InstallationKeyName {
		return v, err
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if len(c.f.script) == 0 {
		return v, err
	}
	step := c.f.script[0]
	c.f.script = c.f.script[1:]
	return step(v, err)
}

func (c faultCustody) Set(ctx context.Context, name string, value []byte) error {
	if name == secrets.InstallationKeyName {
		c.f.mu.Lock()
		c.f.creates++
		c.f.mu.Unlock()
	}
	return c.Custody.Set(ctx, name, value)
}

// installKeyEnv is one test's isolated vault.
type installKeyEnv struct {
	vaultDir string
	faults   *keyFaults
}

// isolateInstallationKey is the MA-2 helper: it clears and restores the
// process firewall memo (under its own mutex, with the selector) and the
// signer memo, and points selectProcessCustody at a guarded file vault over
// t.TempDir() wrapped in faultCustody.
func isolateInstallationKey(t *testing.T) *installKeyEnv {
	t.Helper()
	resetProcessFirewall(t)
	resetInstallationSignerMemo(t)
	env := &installKeyEnv{vaultDir: t.TempDir(), faults: &keyFaults{}}
	inner := testProcessCustody(env.vaultDir)
	selectProcessCustody = func(cfg secrets.Config) (secrets.Custody, error) {
		c, err := inner(cfg)
		if err != nil {
			return nil, err
		}
		return faultCustody{Custody: c, f: env.faults}, nil
	}
	return env
}

// resetInstallationSignerMemo clears the signer memo now and restores it.
func resetInstallationSignerMemo(t *testing.T) {
	t.Helper()
	installationSignerState.mu.Lock()
	saved := installationSignerState.signer
	installationSignerState.signer = nil
	installationSignerState.mu.Unlock()
	t.Cleanup(func() {
		installationSignerState.mu.Lock()
		installationSignerState.signer = saved
		installationSignerState.mu.Unlock()
	})
}

// clearSignerMemo empties the signer memo inside one test.
func clearSignerMemo() {
	installationSignerState.mu.Lock()
	installationSignerState.signer = nil
	installationSignerState.mu.Unlock()
}

// signerMemoSet reports whether the signer memo holds a signer.
func signerMemoSet() bool {
	installationSignerState.mu.Lock()
	defer installationSignerState.mu.Unlock()
	return installationSignerState.signer != nil
}

// entries counts the helper vault's entries; a list error fails the test.
func (e *installKeyEnv) entries(t *testing.T) int {
	t.Helper()
	c, err := testProcessCustody(e.vaultDir)(hostCustodyConfig(e.vaultDir))
	if err != nil {
		t.Fatalf("opening the helper vault: %v", err)
	}
	names, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("listing the helper vault: %v", err)
	}
	return len(names)
}

// backing snapshots every file in the helper vault directory.
func (e *installKeyEnv) backing(t *testing.T) map[string][]byte {
	t.Helper()
	files, err := os.ReadDir(e.vaultDir)
	if err != nil {
		t.Fatalf("reading the helper vault dir: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		if f.IsDir() || f.Name() == ".vault.lock" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.vaultDir, f.Name())) //nolint:gosec // test temp dir
		if err != nil {
			t.Fatalf("reading %s: %v", f.Name(), err)
		}
		out[f.Name()] = b
	}
	return out
}

// inChain reports whether target itself is on err's unwrap chain (identity,
// not errors.Is, which compares Kind only for cascade errors).
func inChain(err, target error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == target {
			return true
		}
	}
	return false
}

// mustPublicKey provisions (or reads) the key and fails the test on error.
func mustPublicKey(t *testing.T, paths runtime.PathProvider) ed25519.PublicKey {
	t.Helper()
	pub, err := installationPublicKey(context.Background(), paths)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("installationPublicKey = (%x, %v)", pub, err)
	}
	return pub
}

// accessors are the two entry points every refusal leg checks.
var accessors = []string{"productionEnvelopeSigner", "installationPublicKey"}

// callAccessor calls one entry point and reports whether it returned a
// non-nil value (a signer or a key) and its error.
func callAccessor(ctx context.Context, paths runtime.PathProvider, which string) (bool, error) {
	if which == "productionEnvelopeSigner" {
		s, err := productionEnvelopeSigner(ctx, paths)
		return s != nil, err
	}
	k, err := installationPublicKey(ctx, paths)
	return k != nil, err
}

func TestInstallationKeyCreatedOnce(t *testing.T) {
	env := isolateInstallationKey(t)
	paths := firewallTestPaths(t)
	pub := mustPublicKey(t, paths)
	clearSignerMemo()
	if again := mustPublicKey(t, paths); !bytes.Equal(again, pub) || env.faults.createCount() != 1 || env.entries(t) != 1 {
		t.Fatalf("second provisioning: same key %v, creates %d", bytes.Equal(again, pub), env.faults.createCount())
	}
	malformed := bytes.Repeat([]byte{0x5A}, 31)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize))
	legs := []faultLeg{
		{"transient read", []func([]byte, error) ([]byte, error){func([]byte, error) ([]byte, error) { return nil, errTransientCustody }}, cascade.KindUnavailable, errTransientCustody},
		{"malformed entry (i)", []func([]byte, error) ([]byte, error){func([]byte, error) ([]byte, error) { return append([]byte(nil), malformed...), nil }}, cascade.KindIntegrity, nil},
		{"reader key differs (iii)", []func([]byte, error) ([]byte, error){
			func(v []byte, err error) ([]byte, error) { return v, err },
			func([]byte, error) ([]byte, error) { return append([]byte(nil), other...), nil },
		}, cascade.KindIntegrity, nil},
		{"reader seed inconsistent (iv)", []func([]byte, error) ([]byte, error){
			func(v []byte, err error) ([]byte, error) { return v, err },
			func(v []byte, err error) ([]byte, error) { return append(other.Seed(), v[ed25519.SeedSize:]...), err },
		}, cascade.KindIntegrity, nil},
	}
	for _, leg := range legs {
		runFaultLeg(t, env, paths, pub, leg)
	}
	testCorruptVaultFile(t, env, paths, pub)
}

// faultLeg is one scripted custody fault and the refusal it must produce.
type faultLeg struct {
	name  string
	steps []func([]byte, error) ([]byte, error)
	kind  cascade.Kind
	same  error
}

// runFaultLeg arms leg before each accessor and checks nothing changed.
func runFaultLeg(t *testing.T, env *installKeyEnv, paths runtime.PathProvider, pub ed25519.PublicKey, leg faultLeg) {
	t.Helper()
	before := env.backing(t)
	for _, call := range accessors {
		clearSignerMemo()
		for _, s := range leg.steps {
			env.faults.arm(s)
		}
		got, err := callAccessor(context.Background(), paths, call)
		if err == nil || got || !cascade.HasKind(err, leg.kind) || cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("%s/%s = (value %v, %v), want a %v refusal and nothing", leg.name, call, got, err, leg.kind)
		}
		if leg.same != nil && !inChain(err, leg.same) {
			t.Fatalf("%s/%s: the custody error was not returned unchanged: %v", leg.name, call, err)
		}
		if env.faults.pending() != 0 || signerMemoSet() {
			t.Fatalf("%s/%s: armed reads left %d, memo set %v", leg.name, call, env.faults.pending(), signerMemoSet())
		}
	}
	if env.faults.createCount() != 1 || !reflect.DeepEqual(env.backing(t), before) {
		t.Fatalf("%s: the vault changed (creates %d)", leg.name, env.faults.createCount())
	}
	clearSignerMemo()
	if again := mustPublicKey(t, paths); !bytes.Equal(again, pub) {
		t.Fatalf("%s: the key changed once the fault cleared", leg.name)
	}
}

// testCorruptVaultFile is leg (ii): non-vault bytes over the backing files.
func testCorruptVaultFile(t *testing.T, env *installKeyEnv, paths runtime.PathProvider, pub ed25519.PublicKey) {
	t.Helper()
	ctx := context.Background()
	original := env.backing(t)
	for name := range original {
		if err := os.WriteFile(filepath.Join(env.vaultDir, name), []byte("not a vault file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	corrupt := env.backing(t)
	clearSignerMemo()
	for _, call := range accessors {
		if got, err := callAccessor(ctx, paths, call); err == nil || got || cascade.HasKind(err, cascade.KindPermissionDenied) || cascade.HasKind(err, cascade.KindNotFound) {
			t.Fatalf("a corrupt vault: %s = (value %v, %v), want a custody error", call, got, err)
		}
	}
	if env.faults.createCount() != 1 || !reflect.DeepEqual(env.backing(t), corrupt) || signerMemoSet() {
		t.Fatalf("a corrupt vault was written over (creates %d)", env.faults.createCount())
	}
	for name, b := range original {
		if err := os.WriteFile(filepath.Join(env.vaultDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if again := mustPublicKey(t, paths); !bytes.Equal(again, pub) {
		t.Fatal("the key changed once the vault file was restored")
	}
}
