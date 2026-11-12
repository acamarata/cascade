package nodes

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type designationProbe struct {
	record   Designation
	found    bool
	loadErr  error
	writeErr error
	writes   int
}

func (b *designationProbe) Load() (Designation, bool, error) { return b.record, b.found, b.loadErr }
func (b *designationProbe) Write(d Designation) error {
	b.writes++
	if b.writeErr != nil {
		return b.writeErr
	}
	b.record, b.found = d, true
	return nil
}
func (b *designationProbe) Remove() error { b.record, b.found = Designation{}, false; return nil }

type designationBindingProbe struct{ err error }

func (b designationBindingProbe) Load() (ControllerBinding, bool, error) {
	return ControllerBinding{}, false, b.err
}

func TestHandoverRefusalsNeverCallWrite(t *testing.T) {
	designationTestHome(t)
	for name, stored := range map[string]Designation{
		"absent": {}, "overflow": func() Designation { d := testDesignation(); d.Epoch = ^uint64(0); return d }(),
		"invalid stored": {Epoch: 1},
	} {
		t.Run(name, func(t *testing.T) {
			b := &designationProbe{record: stored, found: name != "absent"}
			next := testDesignation()
			next.Epoch, next.Reason = stored.Epoch+1, "handover"
			if err := Handover(b, next); err == nil {
				t.Fatal("invalid handover accepted")
			}
			if b.writes != 0 {
				t.Fatal("refused handover called Write")
			}
		})
	}
	b := &designationProbe{record: testDesignation(), found: true}
	next := b.record
	next.Epoch++
	wantKind(t, Handover(b, next), cascade.KindInvalidInput)
	next.Reason, next.ControllerNodeID = "handover", ""
	wantKind(t, Handover(b, next), cascade.KindInvalidInput)
	if b.writes != 0 {
		t.Fatal("invalid next record called Write")
	}
	wantKind(t, Handover(nil, next), cascade.KindIntegrity)
}

func TestDesignationBackendErrorsPreserved(t *testing.T) {
	designationTestHome(t)
	sentinel := cascade.New(cascade.KindUnavailable, "designation backend test failure")
	b := &designationProbe{loadErr: sentinel}
	next := testDesignation()
	if err := Handover(b, next); err != sentinel || err.Error() != sentinel.Error() {
		t.Fatalf("load error not preserved: %v", err)
	}
	_, err := Designate(context.Background(), b, designationBindingProbe{}, Identity{NodeID: "node-1"}, nil, nil)
	if err != sentinel || err.Error() != sentinel.Error() {
		t.Fatalf("designate load error not preserved: %v", err)
	}
	b.loadErr, b.record, b.found, b.writeErr = nil, testDesignation(), true, sentinel
	next.Epoch, next.Reason = b.record.Epoch+1, "handover"
	if err := Handover(b, next); err != sentinel || err.Error() != sentinel.Error() {
		t.Fatalf("write error not preserved: %v", err)
	}
	_, err = Designate(context.Background(), b, designationBindingProbe{err: sentinel}, Identity{NodeID: "node-1"}, nil, nil)
	wrapped, ok := err.(*cascade.Error)
	if !ok || wrapped.Kind != cascade.KindIntegrity || wrapped.Err != sentinel {
		t.Fatalf("binding load error = %v", err)
	}
}

func TestDesignateCanceledAndMissingBackends(t *testing.T) {
	designationTestHome(t)
	b := &designationProbe{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Designate(ctx, b, designationBindingProbe{}, Identity{NodeID: "node-1"}, nil, nil)
	wantKind(t, err, cascade.KindCanceled)
	_, err = Designate(context.Background(), nil, designationBindingProbe{}, Identity{}, nil, nil)
	wantKind(t, err, cascade.KindIntegrity)
	_, err = Designate(context.Background(), b, nil, Identity{}, nil, nil)
	wantKind(t, err, cascade.KindIntegrity)
	if b.writes != 0 {
		t.Fatal("refused designate called Write")
	}
}
