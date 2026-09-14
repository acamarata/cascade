package plugins

import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose (this file): the builtin registry, read where it is actually
//   POPULATED, carries only builtin-tier plugins — and specifically no
//   process-tier plugin such as cascade-github.
// SPORT: internal/plugins tests (ADD) — P1-E25-W5-S51-T1.

// processTierPlugins are the first-party plugins that ship as their own
// binaries, launched from a manifest by ProcessRuntime. None of them may
// appear in the compile-time builtin registry: an entry here would give the
// plugin a second identity that never passes through the trust gate a
// process-tier install requires, and the host would then have two different
// answers to "what is cascade-github".
var processTierPlugins = []string{"cascade-github"}

// loadedRegistrations loads the registry this test binary links and fails
// if it came back empty.
//
// The emptiness guard is the point. Every assertion below is of the form
// "the registry contains no X", which an empty registry satisfies for free.
// This package links the first-party builtins through registry.go and the
// *_wiring.go files, so an empty registry here means the linkage broke, not
// that the invariant holds.
//
// Load's error is ignored deliberately: registry_test.go's init registers a
// manifest that Load is SUPPOSED to reject, so this binary's Load always
// reports one. TestBuiltinRegistry_Load is what asserts that error.
func loadedRegistrations(t *testing.T) []plugin.BuiltinRegistration {
	t.Helper()
	list := loadedRegistry(t).List()
	if len(list) == 0 {
		t.Fatal("the builtin registry loaded empty; every assertion below would pass vacuously")
	}
	return list
}

// TestBuiltinRegistryCarriesNoProcessTierPlugin is the contract's explicit
// assertion (P1-E25-W5-S51-T1: "a test asserts the builtin registry carries
// no cascade-github entry"), made against a registry that really holds the
// first-party builtins.
func TestBuiltinRegistryCarriesNoProcessTierPlugin(t *testing.T) {
	list := loadedRegistrations(t)
	for _, reg := range list {
		for _, banned := range processTierPlugins {
			if reg.Manifest.ID == banned {
				t.Errorf("%q is in the compile-time builtin registry; it is process tier "+
					"and is launched from its manifest, so it must never be registered here", banned)
			}
		}
	}
}

// TestEveryBuiltinRegistrationDeclaresBuiltinRuntime is the general rule the
// case above is one instance of. It catches the next process-tier or wasm
// plugin that registers itself at compile time, without that plugin having
// to be named in processTierPlugins first.
func TestEveryBuiltinRegistrationDeclaresBuiltinRuntime(t *testing.T) {
	for _, reg := range loadedRegistrations(t) {
		if reg.Manifest.Runtime != plugin.RuntimeBuiltin {
			t.Errorf("%s is in the builtin registry with runtime %q; only %q belongs here",
				reg.Manifest.ID, reg.Manifest.Runtime, plugin.RuntimeBuiltin)
		}
	}
}
