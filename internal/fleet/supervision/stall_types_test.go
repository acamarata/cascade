package supervision

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestStallKindValid(t *testing.T) {
	valid := []StallKind{StallKindIdle, StallKindBlocked, StallKindOOMWaiting, StallKindExitUnexpected, StallKindGateDenied, StallKindUnknown}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("StallKind(%q).Valid() = false, want true", k)
		}
	}
	if StallKind("bogus").Valid() {
		t.Error(`StallKind("bogus").Valid() = true, want false`)
	}
	if StallKind("").Valid() {
		t.Error(`StallKind("").Valid() = true, want false (zero value is not a member)`)
	}
}

func TestSignalKindValid(t *testing.T) {
	for _, k := range []SignalKind{SignalBlocked, SignalIdleTimeout, SignalGateDenied} {
		if !k.Valid() {
			t.Errorf("SignalKind(%q).Valid() = false, want true", k)
		}
	}
	if SignalKind("future-signal").Valid() {
		t.Error(`SignalKind("future-signal").Valid() = true, want false`)
	}
}

// TestSignalToStallKindMapping asserts the ticket's NORMATIVE table,
// including R-16.73's fail-closed rule: an unrecognized SignalKind maps
// to StallKindBlocked, not to StallKindUnknown (that name is reserved
// for an unavailable SOURCE, not a malformed signal — see stall_types.go's
// header).
func TestSignalToStallKindMapping(t *testing.T) {
	cases := []struct {
		in   SignalKind
		want StallKind
	}{
		{SignalBlocked, StallKindBlocked},
		{SignalIdleTimeout, StallKindIdle},
		{SignalGateDenied, StallKindGateDenied},
		{SignalKind("unrecognized-future-kind"), StallKindBlocked},
		{SignalKind(""), StallKindBlocked},
	}
	for _, c := range cases {
		if got := signalToStallKind(c.in); got != c.want {
			t.Errorf("signalToStallKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStallSignalValidate(t *testing.T) {
	cases := []struct {
		name    string
		sig     StallSignal
		wantErr bool
	}{
		{"valid blocked", StallSignal{Kind: SignalBlocked, SessionID: "s1"}, false},
		{"valid idle-timeout", StallSignal{Kind: SignalIdleTimeout, SessionID: "s1"}, false},
		{"valid gate-denied", StallSignal{Kind: SignalGateDenied, SessionID: "s1", JobID: "j1"}, false},
		{"missing session id", StallSignal{Kind: SignalBlocked, SessionID: ""}, true},
		{"gate-denied missing job id", StallSignal{Kind: SignalGateDenied, SessionID: "s1", JobID: ""}, true},
		{"unrecognized kind still valid (fail-closed, not refused)", StallSignal{Kind: SignalKind("unrecognized"), SessionID: "s1"}, false},
	}
	for _, c := range cases {
		err := c.sig.Validate()
		if c.wantErr && err == nil {
			t.Errorf("%s: Validate() = nil, want error", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", c.name, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidSignal) {
			t.Errorf("%s: error %v does not wrap ErrInvalidSignal", c.name, err)
		}
	}
}

func TestErrSourceUnavailableIsKindUnavailable(t *testing.T) {
	if !cascade.HasKind(ErrSourceUnavailable, cascade.KindUnavailable) {
		t.Error("ErrSourceUnavailable does not carry cascade.KindUnavailable")
	}
}
