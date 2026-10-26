package rpc

// Purpose (this file): the elevation table accepts a new verb only through
//   RegisterElevatedVerb, refuses every misuse with a distinct panic, and
//   closes to registration the moment it is read or dispatched through.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// testRegisteredElevatedVerb is a verb this test package adds to the table at
// init, the way a later ticket's own file would. Tests that compare the table
// with the spec set it aside.
const testRegisteredElevatedVerb = "test.registered_elevated_verb"

func init() { RegisterElevatedVerb(testRegisteredElevatedVerb, ElevationRule{Always: true}) }

// expectPanic runs fn and fails unless it panics with a message holding want.
func expectPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		got := recover()
		msg, _ := got.(string)
		if got == nil || !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want a message containing %q", got, want)
		}
	}()
	fn()
}

// unsealForTest reopens the table as a fresh process would find it and
// restores the sealed state afterwards.
func unsealForTest(t *testing.T) {
	t.Helper()
	was := elevationSealed.Load()
	elevationSealed.Store(false)
	t.Cleanup(func() { elevationSealed.Store(was) })
}

func TestRegisterElevatedVerb(t *testing.T) {
	if !IsElevatedVerb(testRegisteredElevatedVerb) {
		t.Fatal("the verb registered at init is not in the table")
	}
	found := false
	for _, n := range ElevatedVerbNames() {
		found = found || n == testRegisteredElevatedVerb
	}
	if !found {
		t.Error("ElevatedVerbNames does not list the registered verb")
	}

	// Argument checks, each with its own message. All run before the seal check.
	always := ElevationRule{Always: true}
	expectPanic(t, "empty method", func() { RegisterElevatedVerb("", always) })
	expectPanic(t, "duplicate method", func() { RegisterElevatedVerb(testRegisteredElevatedVerb, always) })
	expectPanic(t, "zero rule", func() { RegisterElevatedVerb("test.zero_rule", ElevationRule{}) })
	expectPanic(t, "both set", func() {
		RegisterElevatedVerb("test.both_set", ElevationRule{Always: true, Conditional: func(json.RawMessage) bool { return true }})
	})
	if IsElevatedVerb("test.zero_rule") || IsElevatedVerb("test.both_set") {
		t.Error("a refused registration left a row behind")
	}
	// A refused registration changed nothing: the original row still elevates.
	if !IsElevated(testRegisteredElevatedVerb, nil) {
		t.Error("the registered verb stopped being elevated")
	}
}

func TestRegisterElevatedVerbSealsOnFirstRead(t *testing.T) {
	for name, read := range map[string]func(){
		"IsElevated":        func() { IsElevated("status.get", nil) },
		"IsElevatedVerb":    func() { IsElevatedVerb("status.get") },
		"ElevatedVerbNames": func() { ElevatedVerbNames() },
	} {
		t.Run(name, func(t *testing.T) {
			unsealForTest(t)
			defer func() { // leave no half-registered row if the assertion fails
				_ = recover()
			}()
			read()
			expectPanic(t, "sealed", func() { RegisterElevatedVerb("test.late_"+name, ElevationRule{Always: true}) })
			if IsElevatedVerb("test.late_" + name) {
				t.Error("a verb registered after the seal is in the table")
			}
		})
	}
}

func TestRegisterElevatedVerbSealsOnFirstDispatch(t *testing.T) {
	unsealForTest(t)
	reg := NewRegistry()
	reg.Register("status.get", func(context.Context, json.RawMessage) (any, error) { return "ok", nil })
	reg.Use(ElevationMiddleware(ElevationDeps{}))
	if _, eo := reg.Dispatch(context.Background(), &Request{Method: "status.get"}); eo != nil {
		t.Fatalf("dispatch: %+v", eo)
	}
	expectPanic(t, "sealed", func() { RegisterElevatedVerb("test.late_dispatch", ElevationRule{Always: true}) })
}

// The registered verb behaves like a table row: unattested it is challenged
// and never runs; attested it runs once, with a witness for its own name.
func TestRegisteredVerbRefusedWithoutWitnessRunsWithOne(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"x":1}`
	f := newMWFixture(t, testRegisteredElevatedVerb)
	nonce := f.challenge(t, args)
	if f.calls != 0 {
		t.Fatalf("handler ran %d times before any attestation", f.calls)
	}
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err != nil {
		t.Fatal(err)
	}
	w, ok := VerifiedElevationFrom(f.gotCtx)
	if f.calls != 1 || !ok || w.Method() != testRegisteredElevatedVerb {
		t.Fatalf("calls=%d witness=%+v ok=%v, want one run with a witness for %s", f.calls, w, ok, testRegisteredElevatedVerb)
	}
}

// Any Dispatch seals the table, not only one that reaches the elevation
// middleware: an unknown method returns before middleware runs, and a
// registry with no middleware never reads the table.
func TestRegisterElevatedVerbSealsOnAnyDispatch(t *testing.T) {
	for name, method := range map[string]string{"unknown_method": "no.such_method", "bare_registry": "status.get"} {
		t.Run(name, func(t *testing.T) {
			unsealForTest(t)
			reg := NewRegistry() // no middleware at all
			reg.Register("status.get", func(context.Context, json.RawMessage) (any, error) { return "ok", nil })
			_, _ = reg.Dispatch(context.Background(), &Request{Method: method})
			expectPanic(t, "sealed", func() { RegisterElevatedVerb("test.late_any_"+name, ElevationRule{Always: true}) })
			if IsElevatedVerb("test.late_any_" + name) {
				t.Error("a verb registered after the first Dispatch is in the table")
			}
		})
	}
}
