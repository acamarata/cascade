// Package policy (capability.go): Purpose: the capability registry — the
//
//	enumeration of every named capability the runtime exposes, each with
//	the action class it confers when nothing narrower applies. Nothing in
//	the runtime may be reached by a name this registry does not hold.
//
// Inputs: Capability values supplied at registration (daemon boot from the
//
//	`policy` storage domain in production, directly in tests), and a
//	capability name on every lookup.
//
// Outputs: a Capability, or one of this file's typed refusals. There is no
//
//	third outcome: Lookup never returns a usable zero value alongside a
//	nil error, and never returns a nil error for a name it did not find.
//
// Constraints: FAIL CLOSED on every axis. An unknown name is
//
//	capability-not-found, never a permissive default. A malformed name is
//	rejected at registration AND at lookup, so a name that could never
//	have been registered can also never match. DefaultPolicy is bound to
//	policy.ActionClass (types.go, S-17.T3) and is read through
//	safeActionClass, so an unset or out-of-range class reads as
//	destructive_privileged (L4, deny) rather than as read (L0, allow).
//	Art.1: MemoryRegistry is a real, complete implementation, not a stub.
//
// SPORT: internal/policy Capability/ADDED, CapabilityRegistry/ADDED,
//
//	MemoryRegistry/ADDED (P1-E09-W2-S17-T1).
package policy

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The stable identifier strings for this file's refusals, in the A-T7
// spelling the contract names. They appear in error messages and audit
// rows and must not change once shipped (R-14.152: the taxonomy Kind set
// is frozen at fourteen, so a contract identifier survives as a stable
// string on the error rather than as a new Kind).
const (
	// CodeCapabilityNotFound marks a lookup for a name the registry does
	// not hold, and every downstream refusal that follows from one.
	CodeCapabilityNotFound = "capability-not-found"
)

// maxCapabilityNameLen bounds a capability name. A name is an identifier,
// not a payload; an unbounded one is a log-injection and storage-key
// hazard rather than a legitimate capability.
const maxCapabilityNameLen = 128

// Capability is one named permission the runtime exposes.
//
// The zero value is not a capability: Validate rejects it, and every path
// that admits a Capability into the registry runs Validate first.
type Capability struct {
	// Name is the capability's stable identifier, e.g. "memory.write".
	// Lowercase dot-separated segments of [a-z0-9_-]; see validName.
	Name string `json:"name"`
	// Desc is the human-readable description shown wherever a user is
	// asked to reason about the capability.
	Desc string `json:"desc"`
	// DefaultPolicy is the ActionClass this capability confers when no
	// narrower rule applies. It is an ActionClass (types.go), never a
	// second enum, so the §5.15 ladder has exactly one definition.
	DefaultPolicy ActionClass `json:"default_policy"`
}

// Validate reports whether c may enter the registry. There is no default
// for any field: a capability that omits one is refused rather than being
// assigned a value its author did not write.
func (c Capability) Validate() error {
	if err := validateCapabilityName(c.Name); err != nil {
		return err
	}
	if strings.TrimSpace(c.Desc) == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: capability %q has no description", c.Name)
	}
	if !c.DefaultPolicy.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: capability %q: %q is not an action class",
			c.Name, c.DefaultPolicy.String())
	}
	return nil
}

// Class returns the capability's action class, resolved fail-closed: an
// unset or out-of-range DefaultPolicy reads as destructive_privileged, the
// deny rung, never as read. Callers read this rather than the field, so a
// value that escaped Validate (a row decoded from storage, say) still
// cannot present itself as permissive.
func (c Capability) Class() ActionClass { return safeActionClass(c.DefaultPolicy) }

// validateCapabilityName enforces the name grammar. It runs at
// registration and again at lookup: a name that could not have been
// registered must not be able to match a stored key either, and the
// symmetry is what makes that true without trusting the store.
func validateCapabilityName(name string) error {
	if name == "" {
		return cascade.New(cascade.KindInvalidInput, "policy: capability has no name")
	}
	if len(name) > maxCapabilityNameLen {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: capability name is %d bytes, over the %d-byte limit",
			len(name), maxCapabilityNameLen)
	}
	if !validName(name) {
		return cascade.Newf(cascade.KindInvalidInput,
			"policy: %q is not a well-formed capability name", sanitize(name))
	}
	return nil
}

// validName reports whether s is one or more dot-separated segments of
// [a-z0-9_-], each non-empty. Deny-by-default: the loop admits only the
// characters named here, so any byte the grammar does not list — a path
// separator, a wildcard, a control character, a space — rejects the name.
func validName(s string) bool {
	segments := strings.Split(s, ".")
	for _, seg := range segments {
		if seg == "" {
			return false
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z',
				r >= '0' && r <= '9',
				r == '_', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// sanitize renders an untrusted identifier for an error message without
// echoing a control character or an unbounded string into a log line.
func sanitize(s string) string {
	const limit = 64
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(cleaned) > limit {
		return cleaned[:limit] + "..."
	}
	return cleaned
}

// ErrCapabilityNotFound is the comparison target for every
// capability-not-found refusal. It carries KindNotFound, so a caller that
// only knows the taxonomy still classifies it correctly, and every refusal
// this file builds wraps it, so errors.Is finds it by identity as well as
// by kind.
var ErrCapabilityNotFound = cascade.New(cascade.KindNotFound, CodeCapabilityNotFound)

// newCapabilityNotFound builds the refusal for a name the registry does
// not hold. The name is sanitized before it reaches the message.
func newCapabilityNotFound(name string) error {
	return cascade.Wrapf(cascade.KindNotFound, ErrCapabilityNotFound,
		"policy: %s: no capability named %q", CodeCapabilityNotFound, sanitize(name))
}

// CapabilityRegistry is the set of capabilities the runtime exposes.
//
// Every method is ctx-first (02 §v1.1) and no implementation stores a ctx.
// Lookup is the only read a permission decision may rely on: List is for
// presentation, and a caller must not scan List to decide anything, because
// List's result is a snapshot that a concurrent Remove can outdate.
type CapabilityRegistry interface {
	// Add registers c. A capability that fails Validate is refused, and a
	// name already present is a conflict rather than an overwrite:
	// silently replacing a capability would silently replace the action
	// class it confers.
	Add(ctx context.Context, c Capability) error

	// Remove deletes the capability named name. Removing a name that is
	// not registered is capability-not-found, not a silent success — a
	// caller that thinks it revoked something needs to hear that it did
	// not.
	Remove(ctx context.Context, name string) error

	// Lookup returns the capability named name, or capability-not-found.
	// It never returns a usable Capability alongside a non-nil error.
	Lookup(ctx context.Context, name string) (Capability, error)

	// List returns every registered capability, ordered by name so the
	// result is stable across calls and across builds.
	List(ctx context.Context) ([]Capability, error)
}

// MemoryRegistry is the in-memory CapabilityRegistry: a real, complete
// implementation (Art.1 — not a stub), safe for concurrent use, used by
// tests and by any caller assembling a registry before the daemon's
// `policy`-domain-backed one exists.
type MemoryRegistry struct {
	mu   sync.RWMutex
	caps map[string]Capability
}

// NewMemoryRegistry returns an empty registry. An empty registry answers
// every Lookup with capability-not-found, which is the correct empty-set
// behavior: nothing registered means nothing permitted, not everything.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{caps: map[string]Capability{}}
}

var _ CapabilityRegistry = (*MemoryRegistry)(nil)

// Add implements CapabilityRegistry.
func (r *MemoryRegistry) Add(_ context.Context, c Capability) error {
	if err := c.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.caps[c.Name]; exists {
		return cascade.Newf(cascade.KindConflict,
			"policy: capability %q is already registered", c.Name)
	}
	r.caps[c.Name] = c
	return nil
}

// Remove implements CapabilityRegistry.
func (r *MemoryRegistry) Remove(_ context.Context, name string) error {
	if err := validateCapabilityName(name); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.caps[name]; !exists {
		return newCapabilityNotFound(name)
	}
	delete(r.caps, name)
	return nil
}

// Lookup implements CapabilityRegistry. A malformed name is rejected
// before the map is consulted, so a name the grammar forbids can never
// match a key however the map came to hold it.
func (r *MemoryRegistry) Lookup(_ context.Context, name string) (Capability, error) {
	if err := validateCapabilityName(name); err != nil {
		return Capability{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.caps[name]
	if !ok {
		return Capability{}, newCapabilityNotFound(name)
	}
	return c, nil
}

// List implements CapabilityRegistry.
func (r *MemoryRegistry) List(_ context.Context) ([]Capability, error) {
	r.mu.RLock()
	out := make([]Capability, 0, len(r.caps))
	for _, c := range r.caps {
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
