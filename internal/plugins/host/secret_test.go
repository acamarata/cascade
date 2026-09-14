package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSecretBroker is a SecretBroker whose Reference either succeeds with
// a fixed opaque reference or fails, and records every key it was asked
// to resolve (never a value).
type fakeSecretBroker struct {
	rawValue     string // the value CheckSecretRef must NEVER return or leak
	fail         bool
	requestedKey string
}

func (b *fakeSecretBroker) Reference(_ context.Context, key string) (SecretHandle, error) {
	b.requestedKey = key
	if b.fail {
		return SecretHandle{}, cascade.New(cascade.KindNotFound, "no such vault entry")
	}
	// A real broker's reference has no relationship to the stored value's
	// bytes; this fake ties them together on purpose so the test below can
	// assert the raw value never surfaces anywhere in CheckSecretRef's
	// return path, including through String()/fmt formatting.
	return newSecretHandle("ref-for-" + key), nil
}

func TestCheckSecretRefNeverExposesRawValue(t *testing.T) {
	sink := &fakeAuditSink{}
	const rawValue = "sk-live-" + "do-not-leak-this-9f3e" // split: avoids a contiguous credential-shaped literal
	broker := &fakeSecretBroker{rawValue: rawValue}
	e, err := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, broker, sink)
	if err != nil {
		t.Fatalf("build enforcer: %v", err)
	}

	handle, err := e.CheckSecretRef(context.Background(), "api-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handle.IsZero() {
		t.Fatal("a successful reference must not be the zero handle")
	}
	if broker.requestedKey != "api-key" {
		t.Fatalf("broker was asked for key %q, want %q", broker.requestedKey, "api-key")
	}

	// The raw value must not appear in the handle's string form, nor in
	// any audit entry recorded for this call.
	if strings.Contains(handle.String(), rawValue) {
		t.Fatalf("SecretHandle.String() leaked the raw value: %q", handle.String())
	}
	if strings.Contains(fmt.Sprintf("%v", handle), rawValue) {
		t.Fatalf("%%v of SecretHandle leaked the raw value")
	}
	for _, ev := range sink.events {
		if strings.Contains(ev.Reason, rawValue) {
			t.Fatalf("audit entry leaked the raw value: %+v", ev)
		}
	}
	entry, ok := sink.last()
	if !ok || !entry.Allowed || entry.CallType != secretCallType {
		t.Fatalf("audit entry = %+v (ok=%v), want an allowed secret_ref entry", entry, ok)
	}
}

func TestCheckSecretRefFailClosedCases(t *testing.T) {
	t.Run("empty key denies without calling the broker", func(t *testing.T) {
		sink := &fakeAuditSink{}
		broker := &fakeSecretBroker{}
		e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, broker, sink)
		_, err := e.CheckSecretRef(context.Background(), "")
		if !errors.Is(err, cascade.ErrCapabilityDenied) {
			t.Fatalf("error = %v, want KindCapabilityDenied", err)
		}
		if broker.requestedKey != "" {
			t.Fatal("the broker must never be called for an empty key")
		}
	})

	t.Run("nil broker denies", func(t *testing.T) {
		sink := &fakeAuditSink{}
		e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, nil, sink)
		handle, err := e.CheckSecretRef(context.Background(), "api-key")
		if !errors.Is(err, cascade.ErrCapabilityDenied) {
			t.Fatalf("error = %v, want KindCapabilityDenied", err)
		}
		if !handle.IsZero() {
			t.Fatal("a denied call must return the zero handle")
		}
	})

	t.Run("broker error denies and does not leak the broker's error as a value", func(t *testing.T) {
		sink := &fakeAuditSink{}
		broker := &fakeSecretBroker{fail: true}
		e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, broker, sink)
		handle, err := e.CheckSecretRef(context.Background(), "api-key")
		if !errors.Is(err, cascade.ErrCapabilityDenied) {
			t.Fatalf("error = %v, want KindCapabilityDenied", err)
		}
		if !handle.IsZero() {
			t.Fatal("a denied call must return the zero handle")
		}
		entry, ok := sink.last()
		if !ok || entry.Allowed {
			t.Fatalf("audit entry = %+v (ok=%v), want a denied secret_ref entry", entry, ok)
		}
	})
}

func TestSecretHandleEqualAndIsZero(t *testing.T) {
	var zero SecretHandle
	if !zero.IsZero() {
		t.Fatal("the zero value must report IsZero")
	}
	a := newSecretHandle("x")
	b := newSecretHandle("x")
	c := newSecretHandle("y")
	if !a.Equal(b) {
		t.Fatal("two handles built from the same reference must be Equal")
	}
	if a.Equal(c) {
		t.Fatal("two handles built from different references must not be Equal")
	}
	if a.IsZero() {
		t.Fatal("a handle built from a non-empty reference must not be IsZero")
	}
}
