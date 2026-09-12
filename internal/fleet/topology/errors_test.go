package topology

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSentinelErrorsWrapFrozenKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind cascade.Kind
	}{
		{"ErrTopologyInvariant", ErrTopologyInvariant, cascade.KindInvalidInput},
		{"ErrTopologyNotFound", ErrTopologyNotFound, cascade.KindNotFound},
		{"ErrTopologyConflict", ErrTopologyConflict, cascade.KindConflict},
		{"ErrLaneRetired", ErrLaneRetired, cascade.KindConflict},
	}
	for _, c := range cases {
		kind, ok := cascade.KindOf(c.err)
		if !ok {
			t.Errorf("%s: KindOf returned ok=false", c.name)
		}
		if kind != c.kind {
			t.Errorf("%s: Kind = %v, want %v", c.name, kind, c.kind)
		}
	}
}

func TestNewInvariantErrWraps(t *testing.T) {
	err := newInvariantErr("lane_class", "bogus", "unknown lane class")
	if !errors.Is(err, ErrTopologyInvariant) {
		t.Error("newInvariantErr result should wrap ErrTopologyInvariant")
	}
	if err.Error() == "" {
		t.Error("error message should not be empty")
	}
}

func TestNewNotFoundErrWraps(t *testing.T) {
	err := newNotFoundErr("account", "acct-1")
	if !errors.Is(err, ErrTopologyNotFound) {
		t.Error("newNotFoundErr result should wrap ErrTopologyNotFound")
	}
}

func TestViolationFields(t *testing.T) {
	v := Violation{Invariant: "credential_one_domain", Entity: "credential", RowID: "cred-1", Detail: "quota_domain_ref is empty"}
	if v.Invariant == "" || v.Entity == "" || v.RowID == "" || v.Detail == "" {
		t.Error("Violation fields should all be populated by the constructing call site")
	}
}
