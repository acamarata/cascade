package sync

import (
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/storage"
)

func TestLookupMandatedMappings(t *testing.T) {
	// R-21.223: conversation -> context, registry/accounts -> config.
	dc, ok := Lookup(storage.DomainContext, "conversation")
	if !ok || dc.Domain != storage.DomainContext {
		t.Fatalf("conversation must map onto DomainContext, got %+v ok=%v", dc, ok)
	}
	dc, ok = Lookup(storage.DomainConfig, "registry")
	if !ok || dc.Domain != storage.DomainConfig {
		t.Fatalf("registry must map onto DomainConfig, got %+v ok=%v", dc, ok)
	}
	dc, ok = Lookup(storage.DomainConfig, "accounts")
	if !ok || dc.Class != ClassServerPrimary {
		t.Fatalf("accounts must be server-primary, got %+v ok=%v", dc, ok)
	}
}

func TestLookupUnregisteredDomainIsLocalOnlyByOmission(t *testing.T) {
	// The vault is structurally absent: no entry exists for it at all.
	if _, ok := Lookup(storage.DomainSecrets, "vault"); ok {
		t.Fatal("the vault domain must never be registered in the sync registry")
	}
	// Any other unlisted domain (e.g. audit) is likewise absent, never a
	// synthesized default — R-16.56.
	if _, ok := Lookup(storage.DomainAudit, "audit"); ok {
		t.Fatal("an unregistered domain must report ok=false, never a synced default")
	}
}

func TestNoDomainClassEscapesTheClosedTwelveDomainSet(t *testing.T) {
	known := map[storage.DomainID]bool{}
	for _, m := range storage.AllDomains {
		known[m.ID] = true
	}
	for _, dc := range AllCoreClasses() {
		if !known[dc.Domain] {
			t.Fatalf("domain class %+v names a domain outside storage.AllDomains — R-21.223 forbids a new SQLite domain", dc)
		}
	}
}

func TestClassValid(t *testing.T) {
	for _, c := range []Class{ClassLocalOnly, ClassSynced, ClassServerPrimary, ClassSyncedAppend} {
		if !c.Valid() {
			t.Fatalf("Class %q should be valid", c)
		}
	}
	if Class("bogus").Valid() {
		t.Fatal("an unknown class string must not validate")
	}
}

func TestPluginClassDefaultsLocalOnly(t *testing.T) {
	dc := PluginClass("myplugin", PluginSyncDecl{})
	if dc.Class != ClassLocalOnly {
		t.Fatalf("an undeclared plugin storage.sync key must resolve local-only, got %q", dc.Class)
	}
	dc = PluginClass("myplugin", PluginSyncDecl{Class: ClassSynced})
	if dc.Class != ClassLocalOnly {
		t.Fatalf("plain 'synced' is core-domain-only; a plugin declaring it must fall back local-only, got %q", dc.Class)
	}
}

func TestPluginClassUnknownStringDefaultsLocalOnly(t *testing.T) {
	dc := PluginClass("myplugin", PluginSyncDecl{Class: Class("bogus")})
	if dc.Class != ClassLocalOnly {
		t.Fatalf("an unrecognized class string must resolve local-only, got %q", dc.Class)
	}
}

func TestPluginClassAcceptsDeclaredClasses(t *testing.T) {
	for _, c := range []Class{ClassSyncedAppend, ClassServerPrimary} {
		dc := PluginClass("myplugin", PluginSyncDecl{Class: c})
		if dc.Class != c {
			t.Fatalf("PluginClass(%q) = %q, want %q", c, dc.Class, c)
		}
		if dc.Domain != storage.DomainConfig {
			t.Fatalf("a plugin-scoped domain must map onto DomainConfig, got %q", dc.Domain)
		}
	}
}

func TestAllCoreClassesSensitivityTiersResolve(t *testing.T) {
	for _, dc := range AllCoreClasses() {
		if dc.Sensitivity.Resolve() == egress.TierUnset {
			t.Fatalf("domain class %+v carries an unresolved sensitivity tier", dc)
		}
	}
}
