//go:build !windows

// Purpose: the fail-closed controller check. Every node-only artifact, every
//
//	probe error and every unusable path provider refuses both accessors with
//	KindPermissionDenied and writes nothing, cold (empty memo) and warm (a
//	signer provisioned earlier in the process). Each setup is first proven
//	visible to internal/nodes, never by a shared path literal.
//
// SPORT: cmd/cascade installation identity eligibility tests (P1-SEC-40).

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// anchor is what nodes.ResolveRole must report for a setup.
type anchor int

const (
	anchorNode anchor = iota
	anchorError
	anchorController // the stub-passing setups nodes reads as controller
	anchorNone       // no data directory to resolve
)

// refusalCase writes one ineligibility into root and returns the provider
// the call uses (root itself, or a broken provider).
type refusalCase struct {
	name   string
	anchor anchor
	setup  func(t *testing.T, root runtime.PathProvider) runtime.PathProvider
}

// dataDirOverride is a provider whose data directory is replaced.
type dataDirOverride struct {
	runtime.PathProvider
	dir string
}

func (d dataDirOverride) DataDir() string { return d.dir }

// writeBinding writes content at the binding path nodes reads.
func writeBinding(content string) func(*testing.T, runtime.PathProvider) runtime.PathProvider {
	return func(t *testing.T, root runtime.PathProvider) runtime.PathProvider {
		t.Helper()
		dir := filepath.Join(root.DataDir(), "nodes")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "controller_binding.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
}

func refusalCases() []refusalCase {
	return []refusalCase{
		{"a resolver says node", anchorNode, writeBinding(`{"endpoint":"tcp://controller.invalid:7443","enrollment_id":"enr-fake"}`)},
		{"b empty file", anchorError, writeBinding("")},
		{"b empty endpoint", anchorController, writeBinding(`{"endpoint":"","enrollment_id":"enr-fake"}`)},
		{"b not json", anchorError, writeBinding("fake binding")},
		{"f binding is a directory", anchorError, func(t *testing.T, root runtime.PathProvider) runtime.PathProvider {
			t.Helper()
			mkdirAll(t, filepath.Join(root.DataDir(), "nodes", "controller_binding.json"))
			return root
		}},
		{"f nodes dir mode 0", anchorError, func(t *testing.T, root runtime.PathProvider) runtime.PathProvider {
			t.Helper()
			if os.Geteuid() == 0 {
				t.Skip("skipped: root reads a 0o000 directory, so the probe cannot fail")
			}
			dir := filepath.Join(root.DataDir(), "nodes")
			mkdirAll(t, dir)
			if err := os.Chmod(dir, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restoring the test dir before removal
			return root
		}},
		{"f nodes is a file (ENOTDIR)", anchorError, func(t *testing.T, root runtime.PathProvider) runtime.PathProvider {
			t.Helper()
			mkdirAll(t, root.DataDir())
			if err := os.WriteFile(filepath.Join(root.DataDir(), "nodes"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"f dangling symlink", anchorController, func(t *testing.T, root runtime.PathProvider) runtime.PathProvider {
			t.Helper()
			dir := filepath.Join(root.DataDir(), "nodes")
			mkdirAll(t, dir)
			if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "controller_binding.json")); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"g nil provider", anchorNone, func(*testing.T, runtime.PathProvider) runtime.PathProvider { return nil }},
		{"g empty data dir", anchorNone, func(_ *testing.T, root runtime.PathProvider) runtime.PathProvider {
			return dataDirOverride{PathProvider: root, dir: ""}
		}},
		{"g relative data dir", anchorNone, func(_ *testing.T, root runtime.PathProvider) runtime.PathProvider {
			return dataDirOverride{PathProvider: root, dir: filepath.Join("relative", "data")}
		}},
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// checkAnchor proves internal/nodes sees the setup the way the case says.
func checkAnchor(t *testing.T, c refusalCase, root runtime.PathProvider) {
	t.Helper()
	if c.anchor == anchorNone {
		return
	}
	role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(root.DataDir()))
	ok := map[anchor]bool{anchorNode: err == nil && role == nodes.RoleNode, anchorError: err != nil,
		anchorController: err == nil && role == nodes.RoleController}[c.anchor]
	if !ok {
		t.Fatalf("%s: nodes.ResolveRole = (%q, %v); the setup is not the one the case names", c.name, role, err)
	}
}

// requireRefused asserts both accessors refuse as ineligible with nothing.
func requireRefused(t *testing.T, env *installKeyEnv, paths runtime.PathProvider, wantEntries int) {
	t.Helper()
	creates := env.faults.createCount()
	for _, call := range accessors {
		got, err := callAccessor(context.Background(), paths, call)
		if got || !cascade.HasKind(err, cascade.KindPermissionDenied) || !inChain(err, errInstallationKeyIneligible) {
			t.Fatalf("%s = (value %v, %v), want the ineligibility refusal and nothing", call, got, err)
		}
	}
	if n := env.entries(t); n != wantEntries || env.faults.createCount() != creates {
		t.Fatalf("a refused call changed the vault: %d entries (want %d), creates %d -> %d", n, wantEntries, creates, env.faults.createCount())
	}
}

func TestNonControllerRefusesProvisioning(t *testing.T) {
	for _, c := range refusalCases() {
		t.Run(c.name+"/cold", func(t *testing.T) {
			env := isolateInstallationKey(t)
			root := firewallTestPaths(t)
			paths := c.setup(t, root)
			checkAnchor(t, c, root)
			requireRefused(t, env, paths, 0)
		})
		t.Run(c.name+"/warm", func(t *testing.T) {
			env := isolateInstallationKey(t)
			root := firewallTestPaths(t)
			mustPublicKey(t, root)
			if !signerMemoSet() {
				t.Fatal("the memo is cold after a successful provisioning")
			}
			paths := c.setup(t, root) // the SAME root that warmed the memo
			checkAnchor(t, c, root)
			requireRefused(t, env, paths, 1)
		})
	}
	t.Run("c no node-records store", func(t *testing.T) {
		if ok, reason := controllerByResolver(nil); ok || reason == "" {
			t.Fatalf("controllerByResolver(nil) = (%v, %q), want refused with a reason", ok, reason)
		}
		if ok, _ := controllerByResolver(fakeBinding{ok: true}); ok {
			t.Fatal("a resolver that says node was accepted")
		}
		if ok, reason := controllerByResolver(fakeBinding{err: errors.New("fake resolver failure")}); ok || !strings.Contains(reason, "fake resolver failure") {
			t.Fatalf("a resolver error = (%v, %q), want refused naming it", ok, reason)
		}
		if ok, reason := controllerByResolver(fakeBinding{err: wrapNotExist()}); ok || reason == "" {
			t.Fatalf("a resolver error wrapping not-exist = (%v, %q), want refused", ok, reason)
		}
	})
	t.Run("e controller that enrolled itself", func(t *testing.T) {
		env := isolateInstallationKey(t)
		root := enrolledControllerRoot(t)
		checkAnchor(t, refusalCase{name: "e", anchor: anchorController}, root)
		mustPublicKey(t, root)
		if env.faults.createCount() != 1 {
			t.Fatalf("creates = %d, want 1", env.faults.createCount())
		}
	})
	t.Run("h probe errors", testProbeErrors)
	t.Run("errno table", testBindingProbeAbsentTable)
}

// fakeBinding is a resolver input for the store half of the check.
type fakeBinding struct {
	ok  bool
	err error
}

func (f fakeBinding) Load() (nodes.ControllerBinding, bool, error) {
	return nodes.ControllerBinding{Endpoint: "tcp://controller.invalid:7443"}, f.ok, f.err
}

// fixedClock is the record store's clock.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

// enrolledControllerRoot is case (e): self_identity.json, a node keystore
// key and one DeviceRecord, and no binding file.
func enrolledControllerRoot(t *testing.T) runtime.PathProvider {
	t.Helper()
	root := firewallTestPaths(t)
	dataDir := root.DataDir()
	mkdirAll(t, dataDir)
	keystore, err := nodes.NewNodeKeystore(secrets.Config{Dir: dataDir, ForceFileVault: true})
	if err != nil {
		t.Fatalf("NewNodeKeystore: %v", err)
	}
	identity, err := nodes.EnsureLocalIdentity(context.Background(), nodes.NewFileSelfIdentityBackend(dataDir), keystore, rand.Reader)
	if err != nil {
		t.Fatalf("EnsureLocalIdentity: %v", err)
	}
	if _, err := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), fixedClock{}).Enroll(identity, nodes.TierController); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	return root
}

// testProbeErrors is case (h): an injected lstat over the case (e) layout.
func testProbeErrors(t *testing.T) {
	root := enrolledControllerRoot(t)
	probe := func(err error) (bool, string, int) {
		calls := 0
		ok, reason := controllerEligibleWith(root, func(string) (os.FileInfo, error) { calls++; return nil, err })
		return ok, reason, calls
	}
	for _, err := range []error{
		&os.PathError{Op: "lstat", Path: "fake", Err: syscall.EIO},
		&os.PathError{Op: "lstat", Path: "fake", Err: syscall.EMFILE},
		&os.PathError{Op: "lstat", Path: "fake", Err: syscall.ELOOP},
		errors.New("probe"),
	} {
		ok, reason, calls := probe(err)
		if calls == 0 {
			t.Fatalf("%v: the injected lstat was never called", err)
		}
		if ok || !strings.Contains(reason, errText(err)) {
			t.Fatalf("%v: eligible=%v reason %q, want refused naming the error", err, ok, reason)
		}
	}
	if ok, reason, calls := probe(wrapNotExist()); !ok || calls == 0 {
		t.Fatalf("a not-exist probe over the case (e) layout = (%v, %q, %d calls), want eligible", ok, reason, calls)
	}
}

// testBindingProbeAbsentTable drives the classifier directly.
func testBindingProbeAbsentTable(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EIO, syscall.EACCES, syscall.EISDIR, syscall.ENOTDIR} {
		absent, reason := bindingProbeAbsent(&os.PathError{Op: "lstat", Path: "fake", Err: errno})
		if absent || !strings.Contains(reason, errno.Error()) {
			t.Fatalf("%v: absent=%v reason %q", errno, absent, reason)
		}
	}
	for _, err := range []error{errors.New("x"), context.Canceled} {
		if absent, reason := bindingProbeAbsent(err); absent || reason == "" {
			t.Fatalf("%v: absent=%v reason %q", err, absent, reason)
		}
	}
	if absent, _ := bindingProbeAbsent(wrapNotExist()); !absent {
		t.Fatal("a wrapped fs.ErrNotExist is not absent")
	}
	if absent, reason := bindingProbeAbsent(nil); absent || reason == "" {
		t.Fatalf("nil: absent=%v reason %q, want present", absent, reason)
	}
}

func wrapNotExist() error { return &os.PathError{Op: "lstat", Path: "fake", Err: os.ErrNotExist} }

// errText is the part of err a reason must name.
func errText(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
