package plugins

import (
	"context"
	"errors"
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

// TestLoadMetadataReportsACorruptRecord covers the decode branch. A record
// that is present but unparseable must be reported as corrupt, never as
// absent: "not installed" would send a caller down the install path and
// overwrite whatever is really there.
func TestLoadMetadataReportsACorruptRecord(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := store.Put(ctx, HostNamespace, metadataKey("demo"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}

	_, ok, err := LoadMetadata(ctx, store, "demo")
	if err == nil {
		t.Fatal("a corrupt record decoded without error")
	}
	if ok {
		t.Error("a corrupt record reported ok=true")
	}
	if kind, typed := cascade.KindOf(err); !typed || kind != cascade.KindIntegrity {
		t.Errorf("error kind = %v (typed=%v), want %v", kind, typed, cascade.KindIntegrity)
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error = %v, want it to say the record is corrupt", err)
	}
}

// TestSaveMetadataRefusesAnEmptyName proves the write path validates too,
// so a record can never be persisted under a name no read path would
// accept back.
func TestSaveMetadataRefusesAnEmptyName(t *testing.T) {
	store := storetest.NewMemStore()
	for _, name := range badNames {
		if err := SaveMetadata(context.Background(), store, PluginMetadata{Name: name}); err == nil {
			t.Errorf("SaveMetadata accepted the malformed name %q", name)
		}
	}
}

// TestChangePermsIsIdempotentInBothDirections covers applyGrantChange's two
// no-op arms: granting a capability already held, and revoking one that is
// already absent. Both must converge rather than duplicating an entry or
// erroring, since a repair script re-applies the desired state blindly.
func TestChangePermsIsIdempotentInBothDirections(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{
		Name: "demo", Enabled: true, Grants: []string{"net.http"},
	}); err != nil {
		t.Fatal(err)
	}

	rec, err := ChangePerms(ctx, store, "demo", "net.http", true, true, false)
	if err != nil {
		t.Fatalf("re-granting an existing capability: %v", err)
	}
	if len(rec.Grants) != 1 {
		t.Fatalf("grants = %v, want the capability present exactly once", rec.Grants)
	}

	rec, err = ChangePerms(ctx, store, "demo", "never-granted", false, true, false)
	if err != nil {
		t.Fatalf("revoking an absent capability: %v", err)
	}
	if len(rec.Grants) != 1 || rec.Grants[0] != "net.http" {
		t.Fatalf("grants = %v, want the untouched original set", rec.Grants)
	}
}

// TestChangePermsPropagatesAnUnreadableStore covers the read branch between
// the guards and the write.
func TestChangePermsPropagatesAnUnreadableStore(t *testing.T) {
	broken := failingStore{MemStore: storetest.NewMemStore(), failGet: true}
	_, err := ChangePerms(context.Background(), broken, "demo", "net.http", true, true, false)
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

// TestRemovePluginPropagatesAnUnreadableStore covers remove's read branch,
// distinct from the delete branch already covered: a remove that cannot
// read must not proceed to delete blind.
func TestRemovePluginPropagatesAnUnreadableStore(t *testing.T) {
	broken := failingStore{MemStore: storetest.NewMemStore(), failGet: true}
	if _, err := RemovePlugin(context.Background(), broken, nil, "demo"); !errors.Is(err, errStoreFailure) {
		t.Fatalf("error = %v, want the store's own failure", err)
	}
}

// TestUpdatePluginRefusesAnUnparseableManifest covers the first gate: a
// candidate manifest that is not a manifest at all must be refused before
// any registry check or store read happens.
func TestUpdatePluginRefusesAnUnparseableManifest(t *testing.T) {
	store := storetest.NewMemStore()
	garbage := []byte("this is not a manifest = = =\x00")
	if _, err := UpdatePlugin(context.Background(), store, nil, nil, garbage, "", garbage, true); err == nil {
		t.Fatal("UpdatePlugin accepted an unparseable manifest")
	}
}

// okRegistry is a RegistryVersionChecker that succeeds, so the
// registry-reachable arm can be exercised.
type okRegistry struct{ version string }

func (r okRegistry) LatestVersion(context.Context, string) (string, error) { return r.version, nil }

// failingRegistry always reports the registry as unreachable.
type failingRegistry struct{}

func (failingRegistry) LatestVersion(context.Context, string) (string, error) {
	return "", errors.New("registry: unreachable")
}

// TestCheckRegistryGracefulCoversAllThreeArms pins the allowed-fail leg. An
// update must proceed whether the registry is absent or unreachable, and it
// must say so in its notice; a reachable registry produces no notice at
// all. Without the no-notice arm asserted, a change that returned the
// notice unconditionally would look correct — every update would just
// quietly claim the registry was unavailable.
func TestCheckRegistryGracefulCoversAllThreeArms(t *testing.T) {
	ctx := context.Background()

	if got := checkRegistryGraceful(ctx, nil, "demo"); got != registryUnavailableNotice {
		t.Errorf("nil registry produced %q, want the graceful notice", got)
	}
	if got := checkRegistryGraceful(ctx, failingRegistry{}, "demo"); got != registryUnavailableNotice {
		t.Errorf("unreachable registry produced %q, want the graceful notice", got)
	}
	if got := checkRegistryGraceful(ctx, okRegistry{version: "9.9.9"}, "demo"); got != "" {
		t.Errorf("reachable registry produced %q, want no notice", got)
	}
}

// TestUpdatePluginReportsTheRegistryNotice proves the notice reaches the
// caller's result rather than being computed and dropped.
func TestUpdatePluginReportsTheRegistryNotice(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	manifestBytes := []byte(builtinManifest)

	res, err := UpdatePlugin(ctx, store, failingRegistry{}, nil, manifestBytes, "", manifestBytes, true)
	if err != nil {
		t.Fatalf("UpdatePlugin: %v", err)
	}
	if res.RegistryNotice != registryUnavailableNotice {
		t.Errorf("RegistryNotice = %q, want the graceful notice", res.RegistryNotice)
	}

	res, err = UpdatePlugin(ctx, store, okRegistry{version: "9.9.9"}, nil, manifestBytes, "", manifestBytes, true)
	if err != nil {
		t.Fatalf("UpdatePlugin with a reachable registry: %v", err)
	}
	if res.RegistryNotice != "" {
		t.Errorf("RegistryNotice = %q for a reachable registry, want none", res.RegistryNotice)
	}
}
