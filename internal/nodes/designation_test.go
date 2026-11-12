package nodes

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func designationTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestDesignationFileBackend(t *testing.T) {
	designationTestHome(t)
	dir := t.TempDir()
	b := NewFileDesignationBackend(dir)
	if _, ok, err := b.Load(); err != nil || ok {
		t.Fatalf("absent load: %v, %v", ok, err)
	}
	d := testDesignation()
	if err := b.Write(d); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nodes", "controller_designation.json")
	if runtime.GOOS != "windows" {
		for p, mode := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("mode = %v, want %v", info.Mode().Perm(), mode)
			}
		}
	}
	source := NewFileControllerBindingBackend(dir).(DesignationSource)
	got, ok, err := source.DesignationBackend().Load()
	if err != nil || !ok || got != d {
		t.Fatalf("binding source = %+v, %v, %v", got, ok, err)
	}
	if err := b.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.Load(); err != nil || ok {
		t.Fatalf("removed load: %v, %v", ok, err)
	}
}

func TestDesignationWriteRefusals(t *testing.T) {
	designationTestHome(t)
	dir := t.TempDir()
	b := NewFileDesignationBackend(dir)
	d := testDesignation()
	if err := b.Write(d); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nodes", "controller_designation.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Designation){
		"installation":   func(d *Designation) { d.InstallationID = "" },
		"controller":     func(d *Designation) { d.ControllerNodeID = " " },
		"epoch":          func(d *Designation) { d.Epoch = 0 },
		"date":           func(d *Designation) { d.DesignatedAt = time.Time{} },
		"reason":         func(d *Designation) { d.Reason = "" },
		"unknown reason": func(d *Designation) { d.Reason = "election" },
	} {
		t.Run(name, func(t *testing.T) {
			next := d
			next.Epoch++
			change(&next)
			wantKind(t, b.Write(next), cascade.KindInvalidInput)
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("invalid write changed file: %v", err)
			}
		})
	}
	for _, epoch := range []uint64{1, 3, 4} {
		next := d
		next.Epoch = epoch
		if epoch == 4 {
			next.InstallationID = "foreign"
		}
		wantKind(t, b.Write(next), cascade.KindConflict)
	}
}

func TestDesignationLoadRejectsMalformedRecords(t *testing.T) {
	designationTestHome(t)
	dir := t.TempDir()
	b := NewFileDesignationBackend(dir)
	if err := b.Write(testDesignation()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nodes", "controller_designation.json")
	valid, err := json.Marshal(testDesignation())
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"unknown":true}`)...)
	for _, raw := range [][]byte{[]byte(`{`), []byte(`null`), []byte(`{}`), unknown, append(valid, []byte(` {}`)...)} {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_, ok, err := b.Load()
		wantKind(t, err, cascade.KindIntegrity)
		if ok {
			t.Fatal("invalid designation reported present")
		}
	}
}

func testDesignation() Designation {
	return Designation{InstallationID: "inst_aaaaaaaaaaaaaaaaaaaaaaaaaa", ControllerNodeID: "node-1",
		Epoch: 3, DesignatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Reason: "init"}
}

func TestHandoverEpochMonotonic(t *testing.T) {
	designationTestHome(t)
	for _, tc := range []struct {
		name    string
		epoch   uint64
		foreign bool
	}{{"next", 4, false}, {"equal", 3, false}, {"lower", 2, false}, {"skip", 5, false}, {"foreign", 4, true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			backend := NewFileDesignationBackend(dir)
			stored := testDesignation()
			if err := backend.Write(stored); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "nodes", "controller_designation.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			next := stored
			next.Epoch, next.ControllerNodeID, next.Reason = tc.epoch, "node-2", "handover"
			if tc.foreign {
				next.InstallationID = "inst_bbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			err = Handover(backend, next)
			if tc.name == "next" {
				if err != nil {
					t.Fatal(err)
				}
				got, ok, err := backend.Load()
				if err != nil || !ok || got != next {
					t.Fatalf("load = %+v, %v, %v", got, ok, err)
				}
				return
			}
			wantKind(t, err, cascade.KindConflict)
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused handover changed disk: %v", readErr)
			}
		})
	}
}
