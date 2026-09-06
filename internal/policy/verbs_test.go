package policy

// Purpose: covers the R-21.207 verb registry and the one authorization
//   middleware. The expectations below are transcribed from the ruling and
//   from 07-CLI-COMMAND-TREE's ✦ column, not read back out of
//   verbRegistry: a table asserted against itself proves nothing.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestVerbRegistryMatchesTheCommandTree asserts the registry's key set and
// each verb's mirror status against the command tree, written out here as
// the spec states it.
func TestVerbRegistryMatchesTheCommandTree(t *testing.T) {
	// method -> mirrored as an MCP tool. The ✦ verbs are the read-only
	// ones; every verb that writes queue or grant state is absent from
	// MCP, and the grant verb is absent because its values never travel.
	want := map[string]bool{
		"approval.list":         true,
		"approval.show":         true,
		"approval.grant":        false,
		"approval.deny":         false,
		"approval.expire":       false,
		"standing_grant.list":   true,
		"standing_grant.create": false,
		"standing_grant.change": false,
		"standing_grant.revoke": false,
		"policy.explain":        true,
		"policy.check":          true,
		"policy.list":           true,
		"policy.audit_query":    true,
	}
	got := RegisteredVerbs()
	if len(got) != len(want) {
		t.Fatalf("the registry holds %d verbs, want %d", len(got), len(want))
	}
	for _, spec := range got {
		mirrored, ok := want[spec.Method]
		if !ok {
			t.Errorf("%q is registered but is not a command-tree verb", spec.Method)
			continue
		}
		if spec.MCPExposed != mirrored {
			t.Errorf("%q mirrored = %v, want %v", spec.Method, spec.MCPExposed, mirrored)
		}
	}
}

// TestRegisteredVerbsIsOrdered pins the stable ordering the surface relies
// on when it prints the verb table.
func TestRegisteredVerbsIsOrdered(t *testing.T) {
	got := RegisteredVerbs()
	for i := 1; i < len(got); i++ {
		if got[i-1].Method >= got[i].Method {
			t.Fatalf("verbs are not ordered: %q came before %q", got[i-1].Method, got[i].Method)
		}
	}
}

// TestElevatedVerbsAreTheCanonicalOnes asserts the three verbs §5.14 names
// as elevated report as elevated, and that the read verbs do not. This is
// what would fail if the standing-grant verbs were registered under names
// the canonical elevation table does not carry: the elevation would
// silently disappear.
func TestElevatedVerbsAreTheCanonicalOnes(t *testing.T) {
	for _, method := range []string{"approval.grant", "standing_grant.create", "standing_grant.change"} {
		spec, err := LookupVerb(method)
		if err != nil {
			t.Fatalf("LookupVerb(%q): %v", method, err)
		}
		if !spec.Elevated() {
			t.Errorf("%q is not elevated; §5.14 names it an elevated verb", method)
		}
		if !rpc.IsElevated(method, nil) {
			t.Errorf("%q is not in the canonical elevation table", method)
		}
	}
	for _, method := range []string{"approval.list", "policy.explain", "standing_grant.list"} {
		spec, err := LookupVerb(method)
		if err != nil {
			t.Fatalf("LookupVerb(%q): %v", method, err)
		}
		if spec.Elevated() {
			t.Errorf("%q is elevated; a read verb must not be", method)
		}
	}
}

// TestMCPToolNamesFollowTheMirrorRule asserts the noun.verb to
// cascade_noun_verb mapping, and that a non-mirrored verb yields no name
// at all rather than a plausible-looking one.
func TestMCPToolNamesFollowTheMirrorRule(t *testing.T) {
	spec, err := LookupVerb("policy.audit_query")
	if err != nil {
		t.Fatalf("LookupVerb: %v", err)
	}
	if got := spec.MCPTool(); got != "cascade_policy_audit_query" {
		t.Errorf("MCPTool = %q, want cascade_policy_audit_query", got)
	}
	grant, err := LookupVerb("approval.grant")
	if err != nil {
		t.Fatalf("LookupVerb: %v", err)
	}
	if got := grant.MCPTool(); got != "" {
		t.Errorf("a verb that is not mirrored produced the tool name %q", got)
	}
}

// TestUnregisteredVerbRefused is the ruling's own named assertion: a verb
// the registry does not hold is refused, never defaulted.
func TestUnregisteredVerbRefused(t *testing.T) {
	for _, method := range []string{"", "approval", "approval.grant ", "approval.list.extra", "vault.get"} {
		if _, err := LookupVerb(method); err == nil {
			t.Errorf("LookupVerb(%q) succeeded; an unregistered verb must be refused", method)
		} else if !errors.Is(err, ErrVerbUnregistered) {
			t.Errorf("LookupVerb(%q) = %v, want ErrVerbUnregistered", method, err)
		} else if !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Errorf("LookupVerb(%q) kind = %v, want policy-denied", method, err)
		}
		if _, err := Authorize(context.Background(), method, okAttestor{}); err == nil {
			t.Errorf("Authorize(%q) succeeded; an unregistered verb must be refused", method)
		}
	}
}

// okAttestor vouches for everything. It stands in for an enrolled helper.
type okAttestor struct{}

func (okAttestor) FreshLocalAttestation(context.Context, string) error { return nil }

// refusingAttestor is an enrolled helper that cannot vouch.
type refusingAttestor struct{ err error }

func (r refusingAttestor) FreshLocalAttestation(context.Context, string) error { return r.err }

// TestAuthorizeAllowsReadVerbsWithoutAttestation proves a read verb needs
// no attestation source at all.
func TestAuthorizeAllowsReadVerbsWithoutAttestation(t *testing.T) {
	spec, err := Authorize(context.Background(), "approval.list", nil)
	if err != nil {
		t.Fatalf("Authorize(approval.list, no attestor) = %v, want nil", err)
	}
	if spec.Method != "approval.list" {
		t.Errorf("Authorize returned the spec for %q", spec.Method)
	}
}

// TestAuthorizeElevatedVerbFailsClosed covers all three refusal paths: no
// attestor enrolled, an attestor that refuses with a bare error, and one
// that refuses with a taxonomy error of its own.
func TestAuthorizeElevatedVerbFailsClosed(t *testing.T) {
	if _, err := Authorize(context.Background(), "approval.grant", nil); err == nil {
		t.Fatal("an elevated verb was authorized with no attestation source")
	} else if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Errorf("no-attestor refusal kind = %v, want elevation-required", err)
	}

	bare := refusingAttestor{err: errors.New("the authenticator is unreachable")}
	if _, err := Authorize(context.Background(), "standing_grant.create", bare); err == nil {
		t.Fatal("an elevated verb was authorized by an attestor that refused")
	} else if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Errorf("bare-error refusal kind = %v, want elevation-required", err)
	}

	typed := refusingAttestor{err: cascade.New(cascade.KindPermissionDenied, "this key is not enrolled")}
	if _, err := Authorize(context.Background(), "standing_grant.change", typed); err == nil {
		t.Fatal("an elevated verb was authorized by an attestor that refused")
	} else if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("typed refusal kind = %v; the attestor's own kind must survive", err)
	}
}

// TestAuthorizeElevatedVerbWithFreshAttestation proves the allow path is
// reachable, so the refusals above are not simply "everything denies".
func TestAuthorizeElevatedVerbWithFreshAttestation(t *testing.T) {
	spec, err := Authorize(context.Background(), "approval.grant", okAttestor{})
	if err != nil {
		t.Fatalf("Authorize with a fresh attestation = %v, want nil", err)
	}
	if !spec.Mutates {
		t.Error("approval.grant is not marked as mutating; it redeems a token")
	}
	if spec.Risk != L3 {
		t.Errorf("approval.grant risk = %s, want L3", spec.Risk)
	}
}
