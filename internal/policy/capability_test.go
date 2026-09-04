// Purpose: the capability registry's contract, asserted against the
// SPEC text (04 §Epic I S-17.T1 and 06 §5.10/§5.15), never against a
// second copy of the implementation's own tables. Every expectation below
// is written out as the literal outcome the spec requires.
//
// SPORT: internal/policy Capability/ADDED, MemoryRegistry/ADDED
// (P1-E09-W2-S17-T1).
package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// readCap is a well-formed capability used wherever a valid one is needed.
func readCap() Capability {
	return Capability{Name: "memory.read", Desc: "read memory entries", DefaultPolicy: ClassRead}
}

// TestCapabilityValidate covers every refusal Validate must make. The
// table names the spec rule each row exercises; nothing here is generated
// from the implementation.
func TestCapabilityValidate(t *testing.T) {
	long := strings.Repeat("a", maxCapabilityNameLen+1)
	cases := []struct {
		name    string
		cap     Capability
		wantErr bool
		rule    string
	}{
		{"valid", readCap(), false, "a fully specified capability is accepted"},
		{"zero value", Capability{}, true, "the zero value is not a capability"},
		{"no name", Capability{Desc: "d", DefaultPolicy: ClassRead}, true, "name is required"},
		{"no desc", Capability{Name: "a.b", DefaultPolicy: ClassRead}, true, "description is required"},
		{"blank desc", Capability{Name: "a.b", Desc: "   ", DefaultPolicy: ClassRead}, true, "a blank description is no description"},
		{"unset policy", Capability{Name: "a.b", Desc: "d"}, true, "DefaultPolicy has no permissive zero value"},
		{"out of range policy", Capability{Name: "a.b", Desc: "d", DefaultPolicy: ActionClass(99)}, true, "an out-of-range action class is refused"},
		{"uppercase name", Capability{Name: "Memory.Read", Desc: "d", DefaultPolicy: ClassRead}, true, "names are lowercase"},
		{"slash in name", Capability{Name: "memory/read", Desc: "d", DefaultPolicy: ClassRead}, true, "a path separator is not in the grammar"},
		{"wildcard name", Capability{Name: "memory.*", Desc: "d", DefaultPolicy: ClassRead}, true, "there is no match-all name"},
		{"empty segment", Capability{Name: "memory..read", Desc: "d", DefaultPolicy: ClassRead}, true, "every segment must be non-empty"},
		{"leading dot", Capability{Name: ".read", Desc: "d", DefaultPolicy: ClassRead}, true, "no empty leading segment"},
		{"control char", Capability{Name: "memory\nread", Desc: "d", DefaultPolicy: ClassRead}, true, "control characters are not in the grammar"},
		{"space", Capability{Name: "memory read", Desc: "d", DefaultPolicy: ClassRead}, true, "spaces are not in the grammar"},
		{"over length", Capability{Name: long, Desc: "d", DefaultPolicy: ClassRead}, true, "names are bounded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cap.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want a refusal (%s)", tc.rule)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil (%s)", err, tc.rule)
			}
		})
	}
}

// TestCapabilityClassFailsClosed asserts the binding this ticket adds
// between Capability.DefaultPolicy and policy.ActionClass: an unset or
// out-of-range class reads as destructive_privileged (L4, deny), never as
// read (L0, allow). The expectations come from 06 §5.15's statement that
// the enums have no permissive zero value.
func TestCapabilityClassFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		in   ActionClass
		want ActionClass
	}{
		{"read passes through", ClassRead, ClassRead},
		{"destructive passes through", ClassDestructivePrivileged, ClassDestructivePrivileged},
		{"zero value denies", ActionClass(0), ClassDestructivePrivileged},
		{"out of range denies", ActionClass(200), ClassDestructivePrivileged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Capability{Name: "a.b", Desc: "d", DefaultPolicy: tc.in}.Class()
			if got != tc.want {
				t.Fatalf("Class() = %s, want %s", got, tc.want)
			}
			if got.Risk() != tc.want.Risk() {
				t.Fatalf("Class().Risk() = %s, want %s", got.Risk(), tc.want.Risk())
			}
		})
	}
	if safeActionClass(ActionClass(0)) != ClassDestructivePrivileged {
		t.Fatal("safeActionClass must map the zero value to destructive_privileged")
	}
}

// TestRegistryAddLookupList covers the registry's happy paths and the
// deterministic ordering List must produce.
func TestRegistryAddLookupList(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryRegistry()

	// An empty registry permits nothing: nothing registered means nothing
	// permitted, not everything.
	if _, err := reg.Lookup(ctx, "memory.read"); !errors.Is(err, cascade.ErrNotFound) {
		t.Fatalf("Lookup on empty registry = %v, want capability-not-found", err)
	}
	list, err := reg.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("List on empty registry = %v, %v; want empty, nil", list, err)
	}

	want := []string{"a.one", "b.two", "c.three"}
	for _, n := range []string{"c.three", "a.one", "b.two"} {
		if err := reg.Add(ctx, Capability{Name: n, Desc: "d", DefaultPolicy: ClassLocalDev}); err != nil {
			t.Fatalf("Add(%q): %v", n, err)
		}
	}
	got, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("List returned %d capabilities, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.Name != want[i] {
			t.Errorf("List[%d].Name = %q, want %q (order must be by name)", i, c.Name, want[i])
		}
	}

	found, err := reg.Lookup(ctx, "b.two")
	if err != nil {
		t.Fatalf("Lookup(b.two): %v", err)
	}
	if found.DefaultPolicy != ClassLocalDev {
		t.Errorf("Lookup returned DefaultPolicy %s, want %s", found.DefaultPolicy, ClassLocalDev)
	}
}

// TestRegistryFailClosed asserts every path that must deny. Each row is a
// distinct fail-closed rule, tested explicitly per hard requirement 1.
func TestRegistryFailClosed(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryRegistry()
	if err := reg.Add(ctx, readCap()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	t.Run("add refuses an invalid capability", func(t *testing.T) {
		if err := reg.Add(ctx, Capability{Name: "bad name", Desc: "d", DefaultPolicy: ClassRead}); err == nil {
			t.Fatal("Add accepted a malformed name")
		}
	})

	t.Run("add refuses a duplicate rather than overwriting", func(t *testing.T) {
		dup := readCap()
		dup.DefaultPolicy = ClassDestructivePrivileged
		if err := reg.Add(ctx, dup); !errors.Is(err, cascade.ErrConflict) {
			t.Fatalf("Add(duplicate) = %v, want a conflict", err)
		}
		still, err := reg.Lookup(ctx, "memory.read")
		if err != nil {
			t.Fatalf("Lookup after refused duplicate: %v", err)
		}
		if still.DefaultPolicy != ClassRead {
			t.Fatal("a refused duplicate must not have replaced the registered class")
		}
	})

	t.Run("lookup of an unknown name is capability-not-found", func(t *testing.T) {
		_, err := reg.Lookup(ctx, "memory.write")
		if !errors.Is(err, cascade.ErrNotFound) {
			t.Fatalf("Lookup(unknown) = %v, want capability-not-found", err)
		}
		if !strings.Contains(err.Error(), CodeCapabilityNotFound) {
			t.Fatalf("refusal %q does not carry the %q code", err, CodeCapabilityNotFound)
		}
	})

	t.Run("lookup of a malformed name never matches", func(t *testing.T) {
		for _, n := range []string{"", "memory/read", "MEMORY.READ", "memory.*", strings.Repeat("x", 500)} {
			if _, err := reg.Lookup(ctx, n); err == nil {
				t.Fatalf("Lookup(%q) succeeded; a malformed name must never match", n)
			}
		}
	})
}

// TestRegistryRemoveFailsClosed covers Remove's own refusals and the
// immediacy of a removal. Split from TestRegistryFailClosed to stay inside
// Art.10.3's 50-line function cap.
func TestRegistryRemoveFailsClosed(t *testing.T) {
	ctx := context.Background()
	reg := NewMemoryRegistry()
	if err := reg.Add(ctx, readCap()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	t.Run("remove of an unknown name is not a silent success", func(t *testing.T) {
		if err := reg.Remove(ctx, "memory.write"); !errors.Is(err, cascade.ErrNotFound) {
			t.Fatalf("Remove(unknown) = %v, want capability-not-found", err)
		}
	})

	t.Run("remove of a malformed name is refused", func(t *testing.T) {
		if err := reg.Remove(ctx, "memory read"); err == nil {
			t.Fatal("Remove accepted a malformed name")
		}
	})

	t.Run("remove takes effect immediately", func(t *testing.T) {
		if err := reg.Remove(ctx, "memory.read"); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, err := reg.Lookup(ctx, "memory.read"); !errors.Is(err, cascade.ErrNotFound) {
			t.Fatalf("Lookup after Remove = %v, want capability-not-found", err)
		}
	})
}

// TestSanitizeBoundsUntrustedText asserts the error-message sanitizer
// strips control characters and bounds length, so a hostile capability
// name cannot forge a log line.
func TestSanitizeBoundsUntrustedText(t *testing.T) {
	got := sanitize("ok\x00\nvalue")
	if strings.ContainsAny(got, "\x00\n") {
		t.Fatalf("sanitize left a control character in %q", got)
	}
	long := sanitize(strings.Repeat("z", 300))
	if len(long) > 70 {
		t.Fatalf("sanitize returned %d bytes, want a bounded string", len(long))
	}
	if !strings.HasSuffix(long, "...") {
		t.Fatalf("a truncated value should be marked as truncated, got %q", long)
	}
}
