package envelope

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestNarrowRefusesWiderChildPerField widens exactly one field per case
// and expects a *NarrowError naming that field.
func TestNarrowRefusesWiderChildPerField(t *testing.T) {
	ks := newTestKeystore(t)
	parent := rootEnvelope(ks.signer(t, "controller"))
	for _, tc := range widenCases(t, ks) {
		t.Run(tc.name, func(t *testing.T) {
			child := childEnvelope(t, parent)
			tc.widen(&child)
			got, err := Narrow(parent, child)
			var ne *NarrowError
			if !errors.As(err, &ne) {
				t.Fatalf("Narrow error = %v, want *NarrowError", err)
			}
			if ne.Field != tc.field {
				t.Fatalf("NarrowError.Field = %q (%s), want %q", ne.Field, ne.Reason, tc.field)
			}
			wantKind(t, err, cascade.KindPermissionDenied, "child widens "+tc.field)
			if !reflect.DeepEqual(got, Envelope{}) {
				t.Fatalf("Narrow returned %+v alongside an error", got)
			}
		})
	}
}

// TestNarrowAcceptsNarrowerChild proves the refusals above are not a
// Narrow that refuses everything: a child at or under the parent in every
// field comes back unchanged.
func TestNarrowAcceptsNarrowerChild(t *testing.T) {
	ks := newTestKeystore(t)
	parent := rootEnvelope(ks.signer(t, "controller"))
	child := childEnvelope(t, parent)
	child.Principal = "principal-2"
	child.Roots = []string{"/a/b/c", "/srv/work"}
	child.Tools = []string{"read"}
	child.Commands = nil
	child.Egress = []string{"model"}
	child.Grants[0].ExpiresAt = t0.Add(time.Minute)
	child.Budget = Budget{WallClock: time.Minute, Tokens: 10, CostMicros: 1, Procs: 1}
	child.Ext = []Extension{{Kind: testKind, Data: json.RawMessage(`{"max":3}`)}}
	child.Sensitivity = provider.SensitivityLocalOnly
	child.ExpiresAt = t0.Add(time.Hour)
	got, err := Narrow(parent, child)
	if err != nil {
		t.Fatalf("Narrow: %v", err)
	}
	if !reflect.DeepEqual(got, child) {
		t.Fatalf("Narrow changed the child:\n got %+v\nwant %+v", got, child)
	}
}

// TestNarrowRootBoundaries covers the slash root, an equal root and
// non-normalised roots (refused before any comparison).
func TestNarrowRootBoundaries(t *testing.T) {
	ks := newTestKeystore(t)
	parent := rootEnvelope(ks.signer(t, "controller"))
	parent.Roots = []string{"/"}
	child := childEnvelope(t, parent)
	child.Roots = []string{"/etc", "/home/u"}
	if _, err := Narrow(parent, child); err != nil {
		t.Fatalf("child under / refused: %v", err)
	}
	for _, bad := range []string{"/a/b/../../etc", "a/b", "/a/b/", "/a//b", "/a/./b", "", "/a\x00b"} {
		child.Roots = []string{bad}
		_, err := Narrow(parent, child)
		wantKind(t, err, cascade.KindInvalidInput, "roots")
	}
}

// TestNarrowRefusesWildcardInParent proves "*" is refused in parent and
// child alike, as a normalisation error, so no wildcard is ever issued.
func TestNarrowRefusesWildcardInParent(t *testing.T) {
	ks := newTestKeystore(t)
	parent := rootEnvelope(ks.signer(t, "controller"))
	child := childEnvelope(t, parent)
	child.Egress = nil
	parent.Egress = []string{"*"}
	_, err := Narrow(parent, child)
	wantKind(t, err, cascade.KindInvalidInput, "egress: the wildcard * is refused")
	child = childEnvelope(t, rootEnvelope(ks.signer(t, "controller")))
	child.Tools = []string{"*"}
	_, err = Narrow(rootEnvelope(ks.signer(t, "controller")), child)
	wantKind(t, err, cascade.KindInvalidInput, "tools: the wildcard * is refused")
}

// TestNarrowRefusesUnregisteredKindInBoth proves a kind present in both
// envelopes but never registered still refuses (fail closed).
func TestNarrowRefusesUnregisteredKindInBoth(t *testing.T) {
	ks := newTestKeystore(t)
	parent := rootEnvelope(ks.signer(t, "controller"))
	parent.Ext = []Extension{{Kind: "test.unregistered", Data: json.RawMessage(`{}`)}}
	child := childEnvelope(t, parent)
	_, err := Narrow(parent, child)
	wantKind(t, err, cascade.KindPermissionDenied, "test.unregistered is not registered")
}

// TestNarrowRefusesNonNormalisedInput covers each normalisation rule.
func TestNarrowRefusesNonNormalisedInput(t *testing.T) {
	ks := newTestKeystore(t)
	parent := rootEnvelope(ks.signer(t, "controller"))
	cases := map[string]func(e *Envelope){
		"unsorted tools":   func(e *Envelope) { e.Tools = []string{"read", "edit"} },
		"duplicate cmd":    func(e *Envelope) { e.Commands = []string{"go", "go"} },
		"empty egress":     func(e *Envelope) { e.Egress = []string{""} },
		"bad tier":         func(e *Envelope) { e.Sensitivity = provider.SensitivityTier(9) },
		"negative budget":  func(e *Envelope) { e.Budget.Tokens = -1 },
		"empty grant verb": func(e *Envelope) { e.Grants[0].Verb = "" },
		"unsorted grants": func(e *Envelope) {
			e.Grants = []GrantRef{{ID: "g2", Verb: "a"}, {ID: "g1", Verb: "a"}}
		},
		"empty ext kind": func(e *Envelope) { e.Ext = []Extension{{Data: json.RawMessage(`{}`)}} },
		"unsorted ext": func(e *Envelope) {
			e.Ext = []Extension{{Kind: "b", Data: json.RawMessage(`1`)}, {Kind: "a", Data: json.RawMessage(`1`)}}
		},
		"year 10000 in UTC": func(e *Envelope) {
			e.ExpiresAt = time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("w", -2*3600))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := childEnvelope(t, parent)
			mutate(&bad)
			_, err := Narrow(parent, bad)
			wantKind(t, err, cascade.KindInvalidInput, "envelope: ")
			_, err = Narrow(bad, childEnvelope(t, parent))
			wantKind(t, err, cascade.KindInvalidInput, "envelope: ")
		})
	}
}

// widenCase widens exactly one field of a child envelope.
type widenCase struct {
	name  string
	field string
	widen func(c *Envelope)
}

// widenCases is one case per field (and per budget component).
func widenCases(t *testing.T, ks testKeystore) []widenCase {
	return []widenCase{
		{"extra root", "roots", func(c *Envelope) { c.Roots = []string{"/a/b", "/srv/work", "/x"} }},
		{"sibling-prefix root", "roots", func(c *Envelope) { c.Roots = []string{"/a/bc", "/srv/work"} }},
		{"extra tool", "tools", func(c *Envelope) { c.Tools = []string{"edit", "read", "shell"} }},
		{"extra command", "cmds", func(c *Envelope) { c.Commands = []string{"curl", "git", "go"} }},
		{"extra egress class", "egress", func(c *Envelope) { c.Egress = []string{"model", "vcs", "web"} }},
		{"later grant expiry", "grants", func(c *Envelope) { c.Grants[0].ExpiresAt = c.Grants[0].ExpiresAt.Add(time.Second) }},
		{"grant not in parent", "grants", func(c *Envelope) { c.Grants[0].Verb = "write" }},
		{"larger budget wall clock", "budget", func(c *Envelope) { c.Budget.WallClock++ }},
		{"larger budget tokens", "budget", func(c *Envelope) { c.Budget.Tokens++ }},
		{"larger budget cost", "budget", func(c *Envelope) { c.Budget.CostMicros++ }},
		{"larger budget procs", "budget", func(c *Envelope) { c.Budget.Procs++ }},
		{"unknown extension kind", "ext", func(c *Envelope) {
			c.Ext = append(c.Ext, Extension{Kind: "test.unknown", Data: json.RawMessage(`{}`)})
		}},
		{"child drops a parent extension", "ext", func(c *Envelope) { c.Ext = nil }},
		{"wider extension data", "ext", func(c *Envelope) {
			c.Ext = []Extension{{Kind: testKind, Data: json.RawMessage(`{"max":11}`)}}
		}},
		{"different fence attempt", "fence", func(c *Envelope) { c.Fence.Attempt++ }},
		{"less restrictive sensitivity", "sens", func(c *Envelope) { c.Sensitivity = provider.SensitivityPublic }},
		{"different issuer", "iss", func(c *Envelope) { c.Issuer = fingerprint(ks.signer(t, "other").PublicKey()) }},
		{"wrong ParentDigest", "parent", func(c *Envelope) { c.ParentDigest = "00" }},
		{"earlier IssuedAt", "iat", func(c *Envelope) { c.IssuedAt = c.IssuedAt.Add(-time.Nanosecond) }},
		{"later ExpiresAt", "exp", func(c *Envelope) { c.ExpiresAt = c.ExpiresAt.Add(time.Nanosecond) }},
		{"different Version", "v", func(c *Envelope) { c.Version = Version + 1 }},
	}
}
