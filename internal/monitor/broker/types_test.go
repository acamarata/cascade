package broker

import (
	"errors"
	"reflect"
	"testing"
)

// TestLedgerTypesExist verifies Beat and Device have the expected exported fields.
func TestLedgerTypesExist(t *testing.T) {
	beatFields := map[string]string{
		"NodeID":    "string",
		"Seq":       "uint64",
		"Timestamp": "time.Time",
	}
	bt := reflect.TypeOf(Beat{})
	for name, wantType := range beatFields {
		f, ok := bt.FieldByName(name)
		if !ok {
			t.Fatalf("Beat missing field %s", name)
		}
		if f.Type.String() != wantType {
			t.Fatalf("Beat.%s type = %s, want %s", name, f.Type.String(), wantType)
		}
	}

	deviceFields := map[string]string{
		"NodeID":   "string",
		"LastBeat": "broker.Beat",
		"Stale":    "bool",
	}
	dt := reflect.TypeOf(Device{})
	for name, wantType := range deviceFields {
		f, ok := dt.FieldByName(name)
		if !ok {
			t.Fatalf("Device missing field %s", name)
		}
		if f.Type.String() != wantType {
			t.Fatalf("Device.%s type = %s, want %s", name, f.Type.String(), wantType)
		}
	}
}

// TestSentinelErrorsDistinct verifies ErrNoDevice and ErrNoFreshLedger are distinct.
func TestSentinelErrorsDistinct(t *testing.T) {
	if ErrNoDevice == nil || ErrNoFreshLedger == nil {
		t.Fatal("sentinel errors must not be nil")
	}
	if errors.Is(ErrNoDevice, ErrNoFreshLedger) {
		t.Fatal("ErrNoDevice and ErrNoFreshLedger must be distinct")
	}
}
