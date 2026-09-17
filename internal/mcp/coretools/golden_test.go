package coretools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenTool is one row of testdata/v1-goldens/tools.json.
type goldenTool struct {
	V1Name      string         `json:"v1_name"`
	Status      string         `json:"status"`
	Name        string         `json:"name,omitempty"`
	Alias       string         `json:"alias,omitempty"`
	RPCMethod   string         `json:"rpc_method,omitempty"`
	Capability  string         `json:"capability,omitempty"`
	DeferredTo  string         `json:"deferred_to,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Description string         `json:"v1_description"`
	Schema      map[string]any `json:"v1_input_schema"`
}

func loadGolden(t *testing.T) []goldenTool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", "tools.json"))
	if err != nil {
		t.Fatalf("read the v1 tool golden: %v", err)
	}
	var doc struct {
		Provenance map[string]string `json:"provenance"`
		Tools      []goldenTool      `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode the v1 tool golden: %v", err)
	}
	if len(doc.Tools) == 0 {
		t.Fatal("the v1 tool golden is empty")
	}
	for _, key := range []string{"harvested_from", "harvested_on", "method", "ticket"} {
		if doc.Provenance[key] == "" {
			t.Fatalf("the golden states no %s; Art.2 requires the capture's provenance", key)
		}
	}
	return doc.Tools
}

// TestMCPToolV1GoldenParity is the Art.8.2 inventory-coverage check: every
// v1 tool is either registered or deferred with a ticket, and the two
// lists agree with the golden about which.
//
// The equation it asserts is the whole point of the golden:
//
//	every v1 tool == registered + deferred
//
// A v1 tool in neither list fails here, which is the case the check
// exists for: a tool quietly dropped on the way from v1 to v2 is
// invisible at runtime and looks exactly like a tool that was never
// promised.
func TestMCPToolV1GoldenParity(t *testing.T) {
	golden := loadGolden(t)
	registered, deferred := indexByV1Name(t)

	for _, g := range golden {
		spec, isRegistered := registered[g.V1Name]
		def, isDeferred := deferred[g.V1Name]
		switch {
		case !isRegistered && !isDeferred:
			t.Errorf("v1 tool %q is neither registered nor deferred: it was dropped silently", g.V1Name)
		case g.Status == "registered" && !isRegistered:
			t.Errorf("the golden says %q is registered; the code defers it", g.V1Name)
		case g.Status == "deferred" && !isDeferred:
			t.Errorf("the golden says %q is deferred; the code registers it", g.V1Name)
		case isRegistered:
			assertGoldenMatchesSpec(t, g, spec)
		case isDeferred && g.DeferredTo != def.Ticket:
			t.Errorf("%q: golden defers to %q, code defers to %q", g.V1Name, g.DeferredTo, def.Ticket)
		}
		delete(registered, g.V1Name)
		delete(deferred, g.V1Name)
	}
	for name := range registered {
		t.Errorf("spec claims v1 tool %q, which the v1 inventory does not contain", name)
	}
	for name := range deferred {
		t.Errorf("deferral names v1 tool %q, which the v1 inventory does not contain", name)
	}
}

// indexByV1Name joins the two live surfaces — what is registered and what
// is deferred — onto the v1 name the golden keys on, failing on any tool
// that appears in both or twice in either.
//
// It is the parity test's setup half, lifted out so neither half runs into
// the function-length cap as the surfaces grow; every check it makes is a
// precondition the comparison below would otherwise read past.
func indexByV1Name(t *testing.T) (map[string]Spec, map[string]Deferral) {
	t.Helper()

	registered := map[string]Spec{}
	for _, s := range Specs() {
		if s.V1Name == "" {
			t.Fatalf("spec %q descends from no v1 tool; the golden has nothing to join it on", s.Name)
		}
		if _, dup := registered[s.V1Name]; dup {
			t.Fatalf("two specs claim v1 tool %q", s.V1Name)
		}
		registered[s.V1Name] = s
	}
	deferred := map[string]Deferral{}
	for _, d := range Deferrals() {
		if d.Ticket == "" || d.Reason == "" {
			t.Fatalf("deferral %q states no ticket or no reason; Art.1.3 requires both", d.V1Name)
		}
		assertDeferralEvidence(t, d)
		if _, dup := deferred[d.V1Name]; dup {
			t.Fatalf("v1 tool %q is deferred twice", d.V1Name)
		}
		if _, both := registered[d.V1Name]; both {
			t.Fatalf("v1 tool %q is both registered and deferred", d.V1Name)
		}
		deferred[d.V1Name] = d
	}
	return registered, deferred
}

// assertGoldenMatchesSpec checks the fields the golden and the spec both
// carry. It deliberately does NOT compare schemas: the golden records v1's,
// the spec declares v2's method's own, and they are allowed to differ —
// see testdata/README.md.
func assertGoldenMatchesSpec(t *testing.T, g goldenTool, spec Spec) {
	t.Helper()
	if g.Name != spec.Name {
		t.Errorf("%q: golden name %q, spec name %q", g.V1Name, g.Name, spec.Name)
	}
	if g.RPCMethod != spec.Method {
		t.Errorf("%q: golden method %q, spec method %q", g.V1Name, g.RPCMethod, spec.Method)
	}
	if g.Capability != spec.Capability {
		t.Errorf("%q: golden capability %q, spec capability %q", g.V1Name, g.Capability, spec.Capability)
	}
	if g.Alias != spec.Alias {
		t.Errorf("%q: golden alias %q, spec alias %q", g.V1Name, g.Alias, spec.Alias)
	}
}

// TestTheInstructionContractToolsAreRegistered pins the two tools the
// ticket names by hand, because their absence is invisible at runtime: an
// instruction file tells a model to call cascade_context_search, and a
// build that does not register it produces a model quietly failing to
// follow its own instructions rather than an error anyone sees.
func TestTheInstructionContractToolsAreRegistered(t *testing.T) {
	byName := map[string]Spec{}
	for _, s := range Specs() {
		byName[s.Name] = s
	}
	search, ok := byName["cascade_context_search"]
	if !ok {
		t.Fatal("cascade_context_search is not registered")
	}
	if search.Alias != "cascade_search" {
		t.Errorf("cascade_context_search alias = %q, want the v1 name cascade_search", search.Alias)
	}
	if search.Method != "recall.query" {
		t.Errorf("cascade_context_search dispatches to %q, want recall.query", search.Method)
	}
	slice, ok := byName["cascade_context_slice"]
	if !ok {
		t.Fatal("cascade_context_slice is not registered")
	}
	if slice.Method != "context.slice" {
		t.Errorf("cascade_context_slice dispatches to %q, want context.slice", slice.Method)
	}
}

// TestNoMutatingToolIsEligibleForTheReadOnlyProfile is this ticket's half
// of R-21.179: it asserts nothing about compact-profile MEMBERSHIP, which
// AK/S-73.T3 and AP/S-82.T1 own, only that every tool this ticket
// registers as mutating is marked as such — so a later profile ticket
// filtering on Mutating gets the truth from here.
func TestNoMutatingToolIsEligibleForTheReadOnlyProfile(t *testing.T) {
	writes := map[string]bool{
		CapabilityMemoryWrite: true,
	}
	for _, s := range Specs() {
		if writes[s.Capability] && !s.Mutating {
			t.Errorf("%q needs %q but is not marked mutating", s.Name, s.Capability)
		}
		if s.Mutating && s.Capability == CapabilityMemoryRead {
			t.Errorf("%q is marked mutating but needs only a read capability", s.Name)
		}
	}
}

// assertDeferralEvidence holds the two non-ticket outcomes to their own
// evidence (R-14.266).
//
// A deferral's Ticket normally names the open ticket that owns the missing
// surface, and the orphan gate checks that ticket is still open. "Retired"
// and "served by a plugin" are not tickets, so nothing downstream can
// check them — which is exactly why they need checking here. Without this,
// either sentinel is a way to make a row stop failing without deciding
// anything, and that is the move the whole deferral list exists to
// prevent.
func assertDeferralEvidence(t *testing.T, d Deferral) {
	t.Helper()
	switch d.Ticket {
	case Retired:
		if !strings.Contains(d.Reason, "RETIRED by R-") {
			t.Errorf("%q is marked retired but its reason names no ruling: %q", d.V1Name, d.Reason)
		}
	case ServedByPlugin:
		if !strings.Contains(d.Reason, "SERVED by ") {
			t.Errorf("%q is marked served-by-plugin but its reason names no surface: %q", d.V1Name, d.Reason)
		}
		if !strings.Contains(d.Reason, "cascade_plugin_") && !strings.Contains(d.Reason, "plugin.") {
			t.Errorf("%q says a plugin serves it but names no tool or method a caller could use: %q",
				d.V1Name, d.Reason)
		}
	default:
		if strings.HasPrefix(d.Ticket, "retired") || strings.HasPrefix(d.Ticket, "served") {
			t.Errorf("%q uses a sentinel-shaped ticket %q that is not one of the declared sentinels",
				d.V1Name, d.Ticket)
		}
	}
}
