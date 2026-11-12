package nodes

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDesignateInitialAndIdempotent(t *testing.T) {
	designationTestHome(t)
	dir := t.TempDir()
	b := NewFileDesignationBackend(dir)
	binding := NewFileControllerBindingBackend(dir)
	clock := runtime.NewFixedClock(testDesignation().DesignatedAt)
	entropy := bytes.NewReader(bytes.Repeat([]byte{0x11}, 32))
	self := Identity{NodeID: "node-1"}
	d, err := Designate(context.Background(), b, binding, self, entropy, clock)
	if err != nil {
		t.Fatal(err)
	}
	if d.Epoch != 1 || d.Reason != "init" || d.ControllerNodeID != self.NodeID || d.DesignatedAt != clock.Now() ||
		d.InstallationID != "inst_ceirceirceirceirceirceirce" {
		t.Fatalf("initial designation = %+v", d)
	}
	if entropy.Len() != 16 {
		t.Fatalf("entropy remaining = %d", entropy.Len())
	}
	path := filepath.Join(dir, "nodes", "controller_designation.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	got, err := Designate(context.Background(), b, binding, self, nil, nil)
	if err != nil || got != d {
		t.Fatalf("idempotent designation = %+v, %v", got, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatalf("idempotent designate wrote to disk: %v", err)
	}
	_, err = Designate(context.Background(), b, binding, Identity{NodeID: "node-2"}, entropy, clock)
	wantKind(t, err, cascade.KindConflict)
}

func TestDesignateEnrolledRefused(t *testing.T) {
	designationTestHome(t)
	dir := t.TempDir()
	binding := NewFileControllerBindingBackend(dir)
	if err := binding.Save(ControllerBinding{Endpoint: "https://controller.invalid", EnrollmentID: "enr-1",
		ControllerPubKeyB64: testControllerKeyB64(t)}); err != nil {
		t.Fatal(err)
	}
	_, err := Designate(context.Background(), NewFileDesignationBackend(dir), binding, Identity{NodeID: "node-1"}, nil, nil)
	wantKind(t, err, cascade.KindConflict)
	if err.Error() != "conflict: nodes: enrolled as a node" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodes", "controller_designation.json")); !os.IsNotExist(err) {
		t.Fatalf("designation written for enrolled node: %v", err)
	}
}

func TestDesignateInvalidInputsLeaveNoRecord(t *testing.T) {
	designationTestHome(t)
	clock := runtime.NewFixedClock(testDesignation().DesignatedAt)
	for _, tc := range []struct {
		name  string
		self  Identity
		rnd   io.Reader
		clock runtime.Clock
		kind  cascade.Kind
	}{
		{"identity", Identity{}, bytes.NewReader(make([]byte, 16)), clock, cascade.KindInvalidInput},
		{"entropy", Identity{NodeID: "node-1"}, nil, clock, cascade.KindInvalidInput},
		{"short entropy", Identity{NodeID: "node-1"}, bytes.NewReader(nil), clock, cascade.KindUnavailable},
		{"clock", Identity{NodeID: "node-1"}, bytes.NewReader(make([]byte, 16)), nil, cascade.KindInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			b := NewFileDesignationBackend(dir)
			_, err := Designate(context.Background(), b, NewFileControllerBindingBackend(dir), tc.self, tc.rnd, tc.clock)
			wantKind(t, err, tc.kind)
			if _, ok, err := b.Load(); err != nil || ok {
				t.Fatalf("invalid designate persisted: %v, %v", ok, err)
			}
		})
	}
}
