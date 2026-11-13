package nodes

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBootstrapDesignationSingleMachineHome(t *testing.T) {
	designationTestHome(t)
	t.Run("first daemon start", testBootstrapFreshHome)
	t.Run("bound once never bootstraps", testBootstrapBoundOnce)
	t.Run("binding files never bootstrap", testBootstrapBindingFiles)
	t.Run("existing designation preserved", testBootstrapExisting)
	t.Run("backend errors preserved", testBootstrapBackendErrors)
	t.Run("supersede only untouched bootstrap", testSupersedeBootstrap)
}

func testBootstrapBindingFiles(t *testing.T) {
	for _, raw := range []string{`{}`, `{"endpoint":"https://controller.invalid"}`, `{`} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "nodes", "controller_binding.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			d := NewFileDesignationBackend(dir)
			got, wrote, err := BootstrapDesignation(context.Background(), d, NewFileControllerBindingBackend(dir), dir,
				Identity{NodeID: "node-1"}, bytes.NewReader(make([]byte, 16)), fixedClock{})
			if err == nil || wrote || got != (Designation{}) {
				t.Fatalf("binding home bootstrapped: %+v, %v, %v", got, wrote, err)
			}
			if raw != `{` && (err != ErrRoleConflict || err.Error() != ErrRoleConflict.Error()) {
				t.Fatalf("binding error = %v, want exact ErrRoleConflict", err)
			}
			if _, found, err := d.Load(); err != nil || found {
				t.Fatalf("binding home gained designation: %v, %v", found, err)
			}
		})
	}
}

func testBootstrapExisting(t *testing.T) {
	dir := t.TempDir()
	d := NewFileDesignationBackend(dir)
	want := testDesignation()
	if err := d.Write(want); err != nil {
		t.Fatal(err)
	}
	got, wrote, err := BootstrapDesignation(context.Background(), d, NewFileControllerBindingBackend(dir), dir,
		Identity{NodeID: "node-2"}, nil, nil)
	if err != nil || wrote || got != want {
		t.Fatalf("existing designation = %+v, %v, %v", got, wrote, err)
	}
	stored, found, err := d.Load()
	if err != nil || !found || stored != want {
		t.Fatalf("existing designation changed: %+v, %v, %v", stored, found, err)
	}
}

func testBootstrapBackendErrors(t *testing.T) {
	sentinel := cascade.New(cascade.KindUnavailable, "bootstrap backend failure")
	for _, stage := range []string{"load", "binding", "write"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			d := &designationProbe{}
			var b ControllerBindingBackend = NewFileControllerBindingBackend(dir)
			switch stage {
			case "load":
				d.loadErr = sentinel
			case "binding":
				b = designationBindingProbe{err: sentinel}
			case "write":
				d.writeErr = sentinel
			}
			got, wrote, err := BootstrapDesignation(context.Background(), d, b, dir, Identity{NodeID: "node-1"},
				bytes.NewReader(make([]byte, 16)), fixedClock{})
			if err != sentinel || err.Error() != sentinel.Error() || wrote || got != (Designation{}) {
				t.Fatalf("%s error = %+v, %v, %v", stage, got, wrote, err)
			}
		})
	}
}

func testSupersedeBootstrap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		epoch  uint64
		recs   int
		jobs   int
	}{
		{"untouched", "bootstrap", 1, 0, 0},
		{"initialized", "init", 1, 0, 0},
		{"handed over", "handover", 2, 0, 0},
		{"advanced", "bootstrap", 2, 0, 0},
		{"devices", "bootstrap", 1, 1, 0},
		{"jobs", "bootstrap", 1, 0, 1},
		{"unknown count", "bootstrap", 1, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			d := NewFileDesignationBackend(dir)
			want := testDesignation()
			want.Reason, want.Epoch = tc.reason, tc.epoch
			if err := d.Write(want); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "nodes", "bound_once")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			err := SupersedeBootstrap(d, tc.recs, tc.jobs)
			checkSupersededBootstrap(t, d, want, tc.name == "untouched", err)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("supersede removed tombstone: %v", err)
			}
		})
	}
}

func checkSupersededBootstrap(t *testing.T, d DesignationBackend, want Designation, removed bool, err error) {
	t.Helper()
	if removed {
		if err != nil {
			t.Fatal(err)
		}
	} else {
		wantKind(t, err, cascade.KindConflict)
		if !strings.Contains(err.Error(), want.InstallationID) || !strings.Contains(err.Error(), want.ControllerNodeID) {
			t.Fatalf("conflict did not name designation: %v", err)
		}
	}
	got, found, err := d.Load()
	if err != nil || found == removed || (!removed && got != want) {
		t.Fatalf("supersede result = %+v, %v, %v", got, found, err)
	}
}

func testBootstrapFreshHome(t *testing.T) {
	dir := t.TempDir()
	d, b := NewFileDesignationBackend(dir), NewFileControllerBindingBackend(dir)
	clock := runtime.NewFixedClock(testDesignation().DesignatedAt)
	rnd := bytes.NewReader(bytes.Repeat([]byte{0x11}, 16))
	self := Identity{NodeID: "node-1"}
	got, wrote, err := BootstrapDesignation(context.Background(), d, b, dir, self, rnd, clock)
	if err != nil || !wrote {
		t.Fatalf("bootstrap = %+v, %v, %v", got, wrote, err)
	}
	if got.Epoch != 1 || got.Reason != "bootstrap" || got.ControllerNodeID != self.NodeID ||
		got.InstallationID != "inst_ceirceirceirceirceirceirce" || got.DesignatedAt != clock.Now() {
		t.Fatalf("bootstrap designation = %+v", got)
	}
	stored, found, err := d.Load()
	if err != nil || !found || stored != got {
		t.Fatalf("persisted designation = %+v, %v, %v", stored, found, err)
	}
	if role, err := ResolveRole(b); err != nil || role != RoleController {
		t.Fatalf("role = %q, %v", role, err)
	}
	path := filepath.Join(dir, "nodes", "controller_designation.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, wrote, err := BootstrapDesignation(context.Background(), d, b, dir, self, nil, nil)
	if err != nil || wrote || again != got {
		t.Fatalf("second start = %+v, %v, %v", again, wrote, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatalf("second start changed designation: %v", err)
	}
}

func testBootstrapBoundOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes", "bound_once")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d, b := NewFileDesignationBackend(dir), NewFileControllerBindingBackend(dir)
	clock := runtime.NewFixedClock(testDesignation().DesignatedAt)
	for attempt := 0; attempt < 2; attempt++ {
		rnd := bytes.NewReader(bytes.Repeat([]byte{0x11}, 16))
		got, wrote, err := BootstrapDesignation(context.Background(), d, b, dir, Identity{NodeID: "node-1"}, rnd, clock)
		if err != ErrRoleConflict || err.Error() != ErrRoleConflict.Error() {
			t.Fatalf("tombstone error = %v, want exact ErrRoleConflict", err)
		}
		wantKind(t, err, cascade.KindIntegrity)
		if wrote || got != (Designation{}) || rnd.Len() != 16 {
			t.Fatalf("tombstone bootstrapped: %+v, %v, entropy remaining %d", got, wrote, rnd.Len())
		}
		if _, found, err := d.Load(); err != nil || found {
			t.Fatalf("tombstone home gained designation: %v, %v", found, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tombstone removed: %v", err)
	}
}
