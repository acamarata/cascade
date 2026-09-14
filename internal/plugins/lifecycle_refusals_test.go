package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): cover the guard clauses every metadata verb runs
//   before it touches a store — name validation, the two elevation gates on
//   perms, and the not-installed case. These decide whether a command
//   refuses or proceeds, and a guard that silently stopped guarding would
//   let a malformed name reach a storage namespace (the exact
//   path-traversal surface the id pattern exists to close).
// SPORT: internal/plugins lifecycle-refusal tests (ADD).

// badNames is exactly what validateName refuses: an empty or
// whitespace-only name. It is deliberately NOT the manifest id pattern —
// see TestValidateNameDoesNotEnforceTheManifestIDPattern below, which
// records what these entry points do and do not guarantee.
var badNames = []string{"", "   ", "\t", "\n "}

// TestValidateNameDoesNotEnforceTheManifestIDPattern records a real and
// easily-misread boundary, found while covering these guards.
//
// A manifest's ID is pattern-checked ([a-z][a-z0-9-]*) by plugin.Validate
// at parse time, and pkg/plugin's own doc explains why: an id becomes a
// storage namespace and a CLI/RPC mount point. But SetEnabled,
// RemovePlugin and ChangePerms take an OPERATOR-SUPPLIED name straight
// from the command line, and validateName refuses only emptiness. A name
// like "../escape" therefore passes the guard and is refused later, by the
// lookup failing, rather than by validation.
//
// That is not a traversal defect today: the name becomes part of a store
// KEY (metadataKey prefixes it) and the production store is SQLite, where
// a key is a text column. It is recorded because the asymmetry is
// invisible otherwise, and because a future file-backed store would turn
// it into one. Asserting it here means a later tightening of validateName
// has to come past this test and say so.
func TestValidateNameDoesNotEnforceTheManifestIDPattern(t *testing.T) {
	notPatternChecked := []string{"../escape", "has/slash", "Has-Capitals", "1leading-digit"}
	for _, name := range notPatternChecked {
		if err := validateName(name); err != nil {
			t.Fatalf("validateName(%q) = %v; this test records that it does NOT pattern-check. "+
				"If validation was deliberately tightened, update this test and the entry points that rely on it", name, err)
		}
	}
	for _, name := range badNames {
		if err := validateName(name); err == nil {
			t.Errorf("validateName(%q) accepted an empty name", name)
		}
	}
}

func TestSetEnabledRefusesAMalformedName(t *testing.T) {
	store := storetest.NewMemStore()
	for _, name := range badNames {
		if _, err := SetEnabled(context.Background(), store, name, true); err == nil {
			t.Errorf("SetEnabled accepted the malformed name %q", name)
		}
	}
}

func TestRemovePluginRefusesAMalformedName(t *testing.T) {
	store := storetest.NewMemStore()
	for _, name := range badNames {
		if _, err := RemovePlugin(context.Background(), store, nil, name); err == nil {
			t.Errorf("RemovePlugin accepted the malformed name %q", name)
		}
	}
}

func TestChangePermsRefusesAMalformedName(t *testing.T) {
	store := storetest.NewMemStore()
	for _, name := range badNames {
		_, err := ChangePerms(context.Background(), store, name, "net.http", true, true, false)
		if err == nil {
			t.Errorf("ChangePerms accepted the malformed name %q", name)
		}
	}
}

// TestSetEnabledAndRemoveReportNotInstalled proves an absent plugin is a
// typed not-found rather than a silent no-op, so a script that disables or
// removes a name it got wrong learns that nothing happened.
func TestSetEnabledAndRemoveReportNotInstalled(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"SetEnabled", func() error { _, err := SetEnabled(ctx, store, "absent", false); return err }},
		{"RemovePlugin", func() error { _, err := RemovePlugin(ctx, store, nil, "absent"); return err }},
	} {
		err := tc.call()
		if err == nil {
			t.Fatalf("%s on an uninstalled plugin succeeded, want a not-found refusal", tc.name)
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
			t.Errorf("%s error kind = %v (typed=%v), want %v", tc.name, kind, ok, cascade.KindNotFound)
		}
	}
}

// TestChangePermsIsElevatedInBothDirections pins R-14.51's elevation rule:
// perms grant AND revoke are both elevated, so both refuse without a
// daemon and both refuse when they cannot prompt. A revoke that skipped
// these gates would let an unattended caller strip a capability with
// nothing able to say no on the operator's behalf.
func TestChangePermsIsElevatedInBothDirections(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{
		Name: "demo", Enabled: true, Grants: []string{"net.http"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, grant := range []bool{true, false} {
		wantVerb := "perms grant"
		if !grant {
			wantVerb = "perms revoke"
		}

		t.Run(wantVerb+"/no daemon", func(t *testing.T) {
			_, err := ChangePerms(ctx, store, "demo", "net.http", grant, false, false)
			if err == nil {
				t.Fatal("succeeded with no daemon available, want a refusal")
			}
			if !strings.Contains(err.Error(), wantVerb) {
				t.Errorf("error = %v, want it to name %q", err, wantVerb)
			}
		})

		t.Run(wantVerb+"/no input", func(t *testing.T) {
			_, err := ChangePerms(ctx, store, "demo", "net.http", grant, true, true)
			if err == nil {
				t.Fatal("succeeded with no input available, want a refusal")
			}
			if !strings.Contains(err.Error(), wantVerb) {
				t.Errorf("error = %v, want it to name %q", err, wantVerb)
			}
		})
	}
}

// TestChangePermsRefusesAnEmptyCapability covers the guard between name
// validation and the store read: an empty capability would otherwise be
// appended to a plugin's grant list as a blank entry.
func TestChangePermsRefusesAnEmptyCapability(t *testing.T) {
	store := storetest.NewMemStore()
	for _, capability := range []string{"", "   ", "\t"} {
		_, err := ChangePerms(context.Background(), store, "demo", capability, true, true, false)
		if err == nil {
			t.Errorf("ChangePerms accepted the empty capability %q", capability)
		}
	}
}

// TestChangePermsReportsNotInstalled covers the branch after the store
// read: the plugin name is well formed and the gates passed, but no record
// exists.
func TestChangePermsReportsNotInstalled(t *testing.T) {
	store := storetest.NewMemStore()
	_, err := ChangePerms(context.Background(), store, "absent", "net.http", true, true, false)
	if err == nil {
		t.Fatal("ChangePerms succeeded for an uninstalled plugin, want a not-found refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("error kind = %v (typed=%v), want %v", kind, ok, cascade.KindNotFound)
	}
}
