// Purpose: the lease sentinels each wrap exactly one frozen Kind, a refusal's
// chain carries the sentinel and its cause by identity (errors.Is compares
// Kind only), and removed lanes are described in a stable order.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestLeaseSentinelKinds(t *testing.T) {
	cases := []struct {
		err  *cascade.Error
		kind cascade.Kind
	}{
		{ErrNoCapableTier, cascade.KindUnavailable}, {ErrSensitivityConstraint, cascade.KindPolicyDenied},
		{ErrTier0Reserved, cascade.KindUnavailable}, {ErrDataClassDenied, cascade.KindPolicyDenied},
		{ErrReservationFailed, cascade.KindUnavailable}, {ErrNoQuotaDomain, cascade.KindUnavailable},
		{ErrLeaseUnknown, cascade.KindNotFound},
	}
	for _, c := range cases {
		if kind, ok := cascade.KindOf(c.err); !ok || kind != c.kind || c.err.Msg == "" {
			t.Errorf("%q kind = (%v, %v), want %v with a message", c.err.Msg, kind, ok, c.kind)
		}
	}
	if !hasSentinel(ErrNoCapableTier, ErrFleetExhausted) {
		t.Error("ErrNoCapableTier must wrap ErrFleetExhausted by identity")
	}
	if hasSentinel(ErrTier0Reserved, ErrNoCapableTier) {
		t.Error("ErrTier0Reserved must not be an ErrNoCapableTier by identity, though both are KindUnavailable")
	}
}

func TestRefuseKeepsSentinelAndCause(t *testing.T) {
	cause := errors.New("disk full")
	err := refuse(ErrReservationFailed, cause, "bind lane %q", "lane-t2")
	want := ErrReservationFailed.Msg + `: bind lane "lane-t2": disk full`
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
	if !hasSentinel(err, ErrReservationFailed) || !errors.Is(err, cause) || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want the sentinel, the cause and the sentinel's kind in the chain", err)
	}
	if hasSentinel(err, ErrNoQuotaDomain) {
		t.Fatal("a refusal must not match a different sentinel of the same kind")
	}
	bare := refuse(ErrLeaseUnknown, nil, "")
	if bare.Error() != ErrLeaseUnknown.Msg || !hasSentinel(bare, ErrLeaseUnknown) {
		t.Fatalf("bare refusal = %q, want the sentinel text alone", bare.Error())
	}
	if hasSentinel(nil, ErrLeaseUnknown) || hasSentinel(errors.New("x"), ErrLeaseUnknown) {
		t.Fatal("hasSentinel matched nil or an unrelated error")
	}
}

func TestDescribeRemovedIsSorted(t *testing.T) {
	got := describeRemoved([]removedLane{{lane: "b", kind: "external-api"}, {lane: "a", kind: "unresolved", reason: "floor: low"}})
	if want := "a (unresolved): floor: low; b (external-api)"; got != want {
		t.Fatalf("describeRemoved = %q, want %q", got, want)
	}
}
