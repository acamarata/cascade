package delegation

// Purpose: mint delegation envelopes from named daemon sources
//
//	(contract:delegation-record). MintRoot builds a root envelope for the
//	owner; MintChild builds a child from its own Sources, copies the
//	parent's Fence, Issuer, Version and every Ext, sets ParentDigest =
//	Digest(parent), and signs only after envelope.Narrow(parent, child)
//	passes. No parameter takes a caller root, tool, egress or grant.
//
// SPORT: delegation/mint/ADD (P1-AGT-11).

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/hex"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// The delegation caps. They alias the jobs store's constants, which the
// store enforces inside InsertDelegation's transaction (one source of
// truth; jobs cannot import this package).
const (
	MaxDepth        = jobs.DelegationMaxDepth
	MaxLiveChildren = jobs.DelegationMaxLiveChildren
)

// OwnerCLI is the principal ID MintRoot accepts for the owner CLI, which
// carries no principal of its own.
const OwnerCLI = "owner-cli"

// egressAgentDriver is the only egress class a P1 delegation envelope
// carries.
const egressAgentDriver = "agent-driver"

// toolsPrefix prefixes the classified ToolPolicy in Envelope.Tools (m-10).
const toolsPrefix = "agent.tools."

// toolPolicies is the closed ToolPolicy set (P1-AGT-01).
var toolPolicies = map[string]bool{"none": true, "readonly": true, "all": true}

// Sources are the named daemon sources an envelope is built from. The
// daemon path fills them from the job row and config, never from RPC
// params.
type Sources struct {
	RepoRoot    string
	InScope     []string
	ReadOnlyDir string
	ToolPolicy  string
	Sensitivity provider.SensitivityTier
	Budget      envelope.Budget
	Now         time.Time
	TTL         time.Duration
}

// MintRoot signs a root envelope for owner (or principal.Principal{ID:
// OwnerCLI}). An owner principal with an expiry caps the envelope's.
func MintRoot(ctx context.Context, s envelope.Signer, owner principal.Principal, src Sources) (envelope.Signed, error) {
	if owner.ID == "" {
		return envelope.Signed{}, invalidf("MintRoot: the owner principal ID is required")
	}
	env, err := fromSources(src, owner.ID)
	if err != nil {
		return envelope.Signed{}, err
	}
	if !owner.ExpiresAt.IsZero() {
		if !src.Now.Before(owner.ExpiresAt) {
			return envelope.Signed{}, cascade.New(cascade.KindPermissionDenied, "delegation: MintRoot: the owner principal has expired")
		}
		if env.ExpiresAt.After(owner.ExpiresAt) {
			env.ExpiresAt = owner.ExpiresAt
		}
	}
	return envelope.Issue(ctx, s, env)
}

// MintChild verifies parent under pinned at src.Now, builds the child for
// childPrincipal from src, copies the parent's Fence, Issuer, Version and
// every Ext, joins Sensitivity with the parent's, and signs it only when
// envelope.Narrow(parent, child) passes. A wider child returns the
// *envelope.NarrowError naming the field.
func MintChild(ctx context.Context, s envelope.Signer, parent envelope.Signed, pinned ed25519.PublicKey, childPrincipal string, src Sources) (envelope.Signed, error) {
	if childPrincipal == "" || childPrincipal == OwnerCLI {
		return envelope.Signed{}, invalidf("MintChild: a child principal ID other than %s is required", OwnerCLI)
	}
	child, err := fromSources(src, childPrincipal)
	if err != nil {
		return envelope.Signed{}, err
	}
	penv, err := envelope.Verify(parent, pinned, src.Now)
	if err != nil {
		return envelope.Signed{}, ruleErr("parent", err)
	}
	pd, err := envelope.Digest(penv)
	if err != nil {
		return envelope.Signed{}, ruleErr("parent", err)
	}
	child.Version, child.Issuer, child.Fence, child.ParentDigest = penv.Version, penv.Issuer, penv.Fence, pd
	child.Ext = make([]envelope.Extension, 0, len(penv.Ext))
	for _, x := range penv.Ext {
		child.Ext = append(child.Ext, envelope.Extension{Kind: x.Kind, Data: append([]byte(nil), x.Data...)})
	}
	child.Sensitivity = provider.JoinSensitivity(src.Sensitivity, penv.Sensitivity)
	if _, err := envelope.Narrow(penv, child); err != nil {
		return envelope.Signed{}, err
	}
	return envelope.Issue(ctx, s, child)
}

// fromSources builds the Sources-derived part of an envelope: a fresh ID,
// principalID, Roots, Tools ["agent.tools.<ToolPolicy>"], no Commands,
// Egress [agent-driver], no Grants, Budget, Sensitivity and the window
// [Now, Now+TTL).
func fromSources(src Sources, principalID string) (envelope.Envelope, error) {
	switch {
	case src.Now.IsZero() || src.TTL <= 0:
		return envelope.Envelope{}, invalidf("Sources need Now and a positive TTL")
	case !toolPolicies[src.ToolPolicy]:
		return envelope.Envelope{}, invalidf("ToolPolicy %q is not one of none, readonly, all", src.ToolPolicy)
	case !src.Sensitivity.Valid():
		return envelope.Envelope{}, invalidf("Sensitivity %d is not a declared tier", src.Sensitivity)
	}
	roots, err := rootsFrom(src)
	if err != nil {
		return envelope.Envelope{}, err
	}
	id, err := newID()
	if err != nil {
		return envelope.Envelope{}, err
	}
	return envelope.Envelope{
		Version: envelope.Version, ID: id, Principal: principalID, Roots: roots,
		Tools: []string{toolsPrefix + src.ToolPolicy}, Commands: []string{},
		Egress: []string{egressAgentDriver}, Grants: []envelope.GrantRef{},
		Budget: src.Budget, Sensitivity: src.Sensitivity,
		IssuedAt: src.Now, ExpiresAt: src.Now.Add(src.TTL),
	}, nil
}

// rootsFrom returns RepoRoot joined with each InScope prefix for a
// mutation, or ReadOnlyDir alone for a read-only delegation. Any other
// combination, a non-absolute root, or an InScope entry that is absolute,
// escapes the repo or carries a glob or backslash, is refused.
func rootsFrom(src Sources) ([]string, error) {
	switch {
	case src.ReadOnlyDir != "" && src.RepoRoot == "" && len(src.InScope) == 0:
		if !cleanAbs(src.ReadOnlyDir) {
			return nil, invalidf("ReadOnlyDir %q is not an absolute clean path", src.ReadOnlyDir)
		}
		return []string{src.ReadOnlyDir}, nil
	case src.ReadOnlyDir != "" || src.RepoRoot == "" || len(src.InScope) == 0:
		return nil, invalidf("Sources need RepoRoot with InScope (mutation) or ReadOnlyDir alone (read-only)")
	case !cleanAbs(src.RepoRoot):
		return nil, invalidf("RepoRoot %q is not an absolute clean path", src.RepoRoot)
	}
	set := map[string]bool{}
	for _, p := range src.InScope {
		c := path.Clean(p)
		if p == "" || path.IsAbs(p) || c == ".." || strings.HasPrefix(c, "../") || strings.ContainsAny(p, "*?[\\\x00") {
			return nil, invalidf("InScope entry %q is not a relative path inside the repo", p)
		}
		set[path.Join(src.RepoRoot, c)] = true
	}
	roots := make([]string, 0, len(set))
	for r := range set {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	return roots, nil
}

// cleanAbs reports whether p is an absolute, clean, slash-separated path.
func cleanAbs(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\\\x00")
}

// newID returns a fresh envelope ID from crypto/rand.
func newID() (string, error) {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "delegation: envelope ID")
	}
	return "dlg_" + hex.EncodeToString(b[:]), nil
}

// ruleErr wraps err under the named verification rule, keeping its Kind
// (KindIntegrity when err carries none) and its chain for errors.As.
func ruleErr(rule string, err error) error {
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindIntegrity
	}
	return cascade.Wrap(kind, err, "delegation: "+rule)
}

// invalidf builds a KindInvalidInput error with the package prefix.
func invalidf(format string, args ...any) error {
	return cascade.Newf(cascade.KindInvalidInput, "delegation: "+format, args...)
}
