//go:build !windows

// Purpose: controller designation at the bind and signer seams. A designated
//
//	controller is never bound, an untouched epoch-one bootstrap is superseded
//	behind the bound_once tombstone, Designate refuses an enrolled node, and
//	the installation signer refuses node, both and none.
//
// SPORT: cmd/cascade/node bind controller-designation tests (ADD).

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// bindHome is an existing node data dir with no designation and an
// elevation gate that allows node.bind.
func bindHome(t *testing.T) (nodeCLIDeps, string) {
	t.Helper()
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(t, nil)
	dataDir := deps.Paths.DataDir()
	mkdirAll(t, dataDir)
	removeTestDesignation(t, dataDir)
	return deps, dataDir
}

// designateHome writes a designation with reason and epoch under dataDir.
func designateHome(t *testing.T, dataDir, reason string, epoch uint64) {
	t.Helper()
	d := testDesignation()
	d.Reason, d.Epoch = reason, epoch
	if err := nodes.NewFileDesignationBackend(dataDir).Write(d); err != nil {
		t.Fatal(err)
	}
}

// homeState snapshots the three files bind may write; absent reads as "".
func homeState(t *testing.T, dataDir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"controller_designation.json", "controller_binding.json", "bound_once"} {
		raw, err := os.ReadFile(filepath.Join(dataDir, "nodes", name)) //nolint:gosec // test temp dir
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out[name] = string(raw)
	}
	return out
}

// runBind runs `node bind` with a fresh, valid binding file.
func runBind(t *testing.T, deps nodeCLIDeps) error {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b := nodes.ControllerBinding{Endpoint: "https://controller.example:7443", EnrollmentID: "enr-test",
		ControllerPubKeyB64: base64.StdEncoding.EncodeToString(pub)}
	cmd, _ := jsonCmd(t)
	return runNodeBind(cmd, deps, writeBindingFile(t, b))
}

// requireBindConflict asserts bind is KindConflict naming want, and that the
// designation, binding and tombstone are unchanged.
func requireBindConflict(t *testing.T, deps nodeCLIDeps, dataDir, want string) {
	t.Helper()
	before := homeState(t, dataDir)
	err := runBind(t, deps)
	if !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), want) {
		t.Fatalf("bind = %v, want KindConflict naming %q", err, want)
	}
	if after := homeState(t, dataDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused bind changed the home:\n%v\n->\n%v", before, after)
	}
}

// writeJobsDB writes <dataDir>/cascade.db with a jobs_job table of n rows.
func writeJobsDB(t *testing.T, dataDir string, n int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dataDir, "cascade.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE jobs_job (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO jobs_job (id) VALUES (?)`, "job-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDesignateRefusesEnrolledNode(t *testing.T) {
	dataDir := t.TempDir()
	writeTestBinding(t, dataDir, nodes.ControllerBinding{Endpoint: "tcp://controller.invalid:7443",
		EnrollmentID: "enr-fake", ControllerPubKeyB64: testControllerKeyB64})
	self, _, err := nodes.GenerateIdentity(deterministicReader{seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	before := homeState(t, dataDir)
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	_, err = nodes.Designate(context.Background(), nodes.NewFileDesignationBackend(dataDir),
		nodes.NewFileControllerBindingBackend(dataDir), self, rand.Reader, clock)
	if !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "enrolled as a node") {
		t.Fatalf("Designate on an enrolled node = %v, want KindConflict", err)
	}
	if after := homeState(t, dataDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused Designate wrote a file:\n%v\n->\n%v", before, after)
	}
}

func TestNodeBindRefusesDesignatedController(t *testing.T) {
	for _, d := range []struct {
		reason string
		epoch  uint64
	}{{"init", 1}, {"handover", 2}, {"bootstrap", 2}} {
		t.Run(d.reason, func(t *testing.T) {
			deps, dataDir := bindHome(t)
			designateHome(t, dataDir, d.reason, d.epoch)
			requireBindConflict(t, deps, dataDir, "designated controller")
		})
	}
}

func TestNodeBindSupersedesUntouchedBootstrap(t *testing.T) {
	for _, withDB := range []bool{false, true} {
		deps, dataDir := bindHome(t)
		designateHome(t, dataDir, "bootstrap", 1)
		if withDB {
			writeJobsDB(t, dataDir, 0)
		}
		if err := runBind(t, deps); err != nil {
			t.Fatalf("bind over an untouched bootstrap (cascade.db %v) = %v", withDB, err)
		}
		st := homeState(t, dataDir)
		if st["controller_designation.json"] != "" || st["controller_binding.json"] == "" || st["bound_once"] == "" {
			t.Fatalf("after bind: %v, want no designation, a binding and the tombstone", st)
		}
		if role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(dataDir)); err != nil || role != nodes.RoleNode {
			t.Fatalf("role after bind = (%q, %v), want node", role, err)
		}
	}
	t.Run("killed after the tombstone", testBindKilledBeforeSave)
}

// testBindKilledBeforeSave stops bind at Save (a directory holds the binding
// path): the tombstone stays and a later daemon start never bootstraps.
func testBindKilledBeforeSave(t *testing.T) {
	deps, dataDir := bindHome(t)
	designateHome(t, dataDir, "bootstrap", 1)
	bindingPath := filepath.Join(dataDir, "nodes", "controller_binding.json")
	mkdirAll(t, bindingPath)
	if err := runBind(t, deps); err == nil {
		t.Fatal("bind saved over a directory at the binding path")
	}
	if err := os.Remove(bindingPath); err != nil {
		t.Fatal(err)
	}
	if st := homeState(t, dataDir); st["bound_once"] == "" || st["controller_designation.json"] != "" {
		t.Fatalf("after the interrupted bind: %v, want the tombstone and no designation", st)
	}
	self, _, err := nodes.GenerateIdentity(deterministicReader{seed: 5})
	if err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	_, wrote, err := nodes.BootstrapDesignation(context.Background(), nodes.NewFileDesignationBackend(dataDir),
		nodes.NewFileControllerBindingBackend(dataDir), dataDir, self, rand.Reader, clock)
	if wrote || !inChain(err, nodes.ErrRoleConflict) {
		t.Fatalf("bootstrap after the tombstone = (wrote %v, %v), want ErrRoleConflict", wrote, err)
	}
	_, err = nodes.ResolveRole(nodes.NewFileControllerBindingBackend(dataDir))
	if !cascade.HasKind(err, cascade.KindIntegrity) || !strings.Contains(err.Error(), "role conflict none") {
		t.Fatalf("role after the interrupted bind = %v, want KindIntegrity none", err)
	}
}

func TestNodeBindRefusesUsedBootstrap(t *testing.T) {
	legs := []struct {
		name  string
		setup func(t *testing.T, deps nodeCLIDeps, dataDir string)
		want  string
	}{
		{"one device record", func(t *testing.T, deps nodeCLIDeps, _ string) { seedRecord(t, deps, nodes.TierController) }, "in use"},
		{"one jobs row", func(t *testing.T, _ nodeCLIDeps, dataDir string) { writeJobsDB(t, dataDir, 1) }, "in use"},
		{"cascade.db is not sqlite", func(t *testing.T, _ nodeCLIDeps, dataDir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dataDir, "cascade.db"), []byte("not a database file, padded to look like one........"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "cascade.db"},
	}
	for _, leg := range legs {
		t.Run(leg.name, func(t *testing.T) {
			deps, dataDir := bindHome(t)
			designateHome(t, dataDir, "bootstrap", 1)
			leg.setup(t, deps, dataDir)
			requireBindConflict(t, deps, dataDir, leg.want)
		})
	}
}

func TestEnvelopeSignerRequiresDesignation(t *testing.T) {
	complete := `{"endpoint":"tcp://controller.invalid:7443","enrollment_id":"enr-fake","controller_pubkey_b64":"` + testControllerKeyB64 + `"}`
	emptyEndpoint := `{"endpoint":"","enrollment_id":"enr-fake"}`
	cases := []struct {
		name, binding, conflict string
		designated              bool
	}{
		{"node", complete, "", false},
		{"both", complete, "role conflict both", true},
		{"none", "", "role conflict none", false},
		{"b empty endpoint", emptyEndpoint, "role conflict binding-incomplete", true},
		{"b empty endpoint, no designation", emptyEndpoint, "role conflict binding-incomplete", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := isolateInstallationKey(t)
			root := firewallTestPaths(t) // a designated controller home
			if !c.designated {
				removeTestDesignation(t, root.DataDir())
			}
			if c.binding != "" {
				writeBinding(c.binding)(t, root)
			}
			role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(root.DataDir()))
			if c.conflict == "" && (err != nil || role != nodes.RoleNode) ||
				c.conflict != "" && (!cascade.HasKind(err, cascade.KindIntegrity) || !strings.Contains(err.Error(), c.conflict)) {
				t.Fatalf("ResolveRole = (%q, %v); the setup is not %q", role, err, c.name)
			}
			requireRefused(t, env, root, 0)
		})
	}
	t.Run("designated controller", func(t *testing.T) {
		env := isolateInstallationKey(t)
		mustPublicKey(t, firewallTestPaths(t))
		if env.faults.createCount() != 1 {
			t.Fatalf("creates = %d, want 1", env.faults.createCount())
		}
	})
}
