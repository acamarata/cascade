// Purpose: Narrow, the one rule for deriving a child envelope from a
//
//	parent: the child is accepted only when every field grants at most
//	what the parent grants.
//
// Inputs: a parent and a child Envelope.
// Outputs: the child unchanged, or a *NarrowError naming the first widened
//
//	field (its Unwrap is KindPermissionDenied); a non-normalised envelope
//	is KindInvalidInput.
//
// Constraints: every field has a check; a field this file does not compare
//
//	is a widening hole. Roots compare at a path-segment boundary, names
//	compare as exact strings (the wildcard "*" is refused by validate).
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// wildcard is the name no tool, command or egress list may carry.
const wildcard = "*"

// NarrowError reports the field in which a child envelope grants more
// than its parent.
type NarrowError struct {
	// Field is the wire name of the widened field (for example "roots").
	Field string
	// Reason says how the child widens it.
	Reason string
}

// Error implements error.
func (e *NarrowError) Error() string {
	return "envelope: child widens " + e.Field + ": " + e.Reason
}

// Unwrap returns a KindPermissionDenied taxonomy error.
func (e *NarrowError) Unwrap() error {
	return cascade.New(cascade.KindPermissionDenied, e.Error())
}

// Narrow returns child when it stays within parent in every field.
func Narrow(parent, child Envelope) (Envelope, error) {
	if child.Version != parent.Version {
		return Envelope{}, &NarrowError{"v", "version differs from the parent's"}
	}
	if err := validate(parent); err != nil {
		return Envelope{}, err
	}
	if err := validate(child); err != nil {
		return Envelope{}, err
	}
	checks := []func(p, c Envelope) *NarrowError{
		narrowIdentity, narrowRoots, narrowNames, narrowGrants,
		narrowBudget, narrowExt, narrowFence, narrowWindow,
	}
	for _, check := range checks {
		if ne := check(parent, child); ne != nil {
			return Envelope{}, ne
		}
	}
	return child, nil
}

// narrowIdentity compares Issuer, ParentDigest and Sensitivity (Version
// is compared first, in Narrow, because validate refuses a foreign one).
func narrowIdentity(p, c Envelope) *NarrowError {
	if c.Issuer != p.Issuer {
		return &NarrowError{"iss", "issuer differs from the parent's"}
	}
	d, err := Digest(p)
	if err != nil || c.ParentDigest != d {
		return &NarrowError{"parent", "parent digest is not Digest(parent)"}
	}
	if c.Sensitivity != p.Sensitivity && !c.Sensitivity.MoreRestrictiveThan(p.Sensitivity) {
		return &NarrowError{"sens", "sensitivity " + c.Sensitivity.String() + " is less restrictive than " + p.Sensitivity.String()}
	}
	return nil
}

// narrowRoots requires every child root at or under a parent root.
func narrowRoots(p, c Envelope) *NarrowError {
	for _, r := range c.Roots {
		if !underAny(r, p.Roots) {
			return &NarrowError{"roots", "root " + r + " is not under a parent root"}
		}
	}
	return nil
}

// underAny reports whether root equals or lies below one of parents at a
// path-segment boundary ("/a/bc" is not under "/a/b").
func underAny(root string, parents []string) bool {
	for _, p := range parents {
		if root == p || p == "/" || strings.HasPrefix(root, p+"/") {
			return true
		}
	}
	return false
}

// narrowNames requires tools, commands and egress classes to be subsets.
func narrowNames(p, c Envelope) *NarrowError {
	pairs := []struct {
		field  string
		parent []string
		child  []string
	}{
		{"tools", p.Tools, c.Tools},
		{"cmds", p.Commands, c.Commands},
		{"egress", p.Egress, c.Egress},
	}
	for _, x := range pairs {
		for _, v := range x.child {
			if !contains(x.parent, v) {
				return &NarrowError{x.field, v + " is not granted by the parent"}
			}
		}
	}
	return nil
}

// contains reports whether vals holds v exactly.
func contains(vals []string, v string) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

// narrowGrants requires each child grant in the parent, expiring no later.
func narrowGrants(p, c Envelope) *NarrowError {
	for _, g := range c.Grants {
		found := false
		for _, pg := range p.Grants {
			if pg.ID == g.ID && pg.Verb == g.Verb {
				found = true
				if g.ExpiresAt.After(pg.ExpiresAt) {
					return &NarrowError{"grants", "grant " + g.ID + " expires after the parent's"}
				}
			}
		}
		if !found {
			return &NarrowError{"grants", "grant " + g.ID + "/" + g.Verb + " is not in the parent"}
		}
	}
	return nil
}

// narrowBudget requires every budget component at most the parent's.
func narrowBudget(p, c Envelope) *NarrowError {
	pb, cb := p.Budget, c.Budget
	switch {
	case cb.WallClock > pb.WallClock:
		return &NarrowError{"budget", "wall clock exceeds the parent's"}
	case cb.Tokens > pb.Tokens:
		return &NarrowError{"budget", "tokens exceed the parent's"}
	case cb.CostMicros > pb.CostMicros:
		return &NarrowError{"budget", "cost exceeds the parent's"}
	case cb.Procs > pb.Procs:
		return &NarrowError{"budget", "processes exceed the parent's"}
	}
	return nil
}

// narrowExt requires the child to carry exactly the parent's extension
// kinds, each registered and passing its narrower. A dropped parent
// extension (such as a delegation budget) widens and is refused.
func narrowExt(p, c Envelope) *NarrowError {
	if len(c.Ext) != len(p.Ext) {
		return &NarrowError{"ext", "child extension kinds differ from the parent's"}
	}
	for i, ce := range c.Ext {
		pe := p.Ext[i]
		if ce.Kind != pe.Kind {
			return &NarrowError{"ext", "child extension kinds differ from the parent's"}
		}
		n := lookupExtension(ce.Kind)
		if n == nil {
			return &NarrowError{"ext", "extension kind " + ce.Kind + " is not registered"}
		}
		if err := n(pe.Data, ce.Data); err != nil {
			return &NarrowError{"ext", "extension " + ce.Kind + ": " + err.Error()}
		}
	}
	return nil
}

// narrowFence requires the identical fencing token.
func narrowFence(p, c Envelope) *NarrowError {
	if c.Fence != p.Fence {
		return &NarrowError{"fence", "fencing token differs from the parent's"}
	}
	return nil
}

// narrowWindow requires the child's validity window inside the parent's:
// it starts no earlier and expires no later.
func narrowWindow(p, c Envelope) *NarrowError {
	if c.IssuedAt.Before(p.IssuedAt) {
		return &NarrowError{"iat", "valid before the parent"}
	}
	if c.ExpiresAt.After(p.ExpiresAt) {
		return &NarrowError{"exp", "expires after the parent"}
	}
	return nil
}
