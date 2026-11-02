//go:build !windows

// Purpose: registers the daemon's "plugin.update" handler, the attested
// commit path for an update `cascade plugin update` reports as needing
// elevation, and for `--reverify`, which re-pins a process plugin's
// executable digest only through the signed registry index.
// Inputs: the daemon wiring's plugin store, clock, paths and config.
// Outputs: "plugin.update" bound to plugins.UpdateRPCHandler wrapped by plugins.WithReverify.
// Constraints: the elevation row (always elevated) and the access class
// (owner only) are declared by their owners, never here. The process
// handshaker is plugins.NewProcessHandshaker (the trusted launch path). The
// reverify index verifier is the registry client's own fetch path
// (Ed25519Verifier with the configured or embedded key, then the rollback
// baseline in the registry cache dir) fed the bytes the call carries; there
// is no second verifier.
// SPORT: cmd/cascade daemon registrations (plugin.update).
package main

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-update", Phase: phaseNamespaces, Order: 40,
	Wire: func(w *daemonWiring) error {
		w.Registry.Register(plugins.UpdateRPCMethod,
			plugins.WithReverify(plugins.UpdateRPCHandler(w.Store, plugins.NewProcessHandshaker), w.Store, pluginIndexVerifier(w)))
		return nil
	},
})

// pluginReverifyKey resolves the registry public key a reverify verifies
// with. Tests replace it to inject a signing key.
var pluginReverifyKey = func(w *daemonWiring) []byte {
	path := ""
	if w.Deps.Config != nil {
		path = w.Deps.Config.Registry.PubkeyPath
	}
	key, _ := resolvePluginRegistryPubkey(path)
	return key
}

// indexBytesFetcher serves the index bytes a call carried to the registry
// client, which verifies them; it fetches no artifact.
type indexBytesFetcher struct{ index []byte }

func (f indexBytesFetcher) FetchIndex(context.Context) ([]byte, error) { return f.index, nil }

func (indexBytesFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnsupported, "plugin.update reverify fetches no artifact")
}

// pluginIndexVerifier builds the IndexVerifier over w: signature, schema and
// the rollback baseline (sequence 0, an older sequence, or the same sequence
// with other bytes is refused) under the registry cache dir.
func pluginIndexVerifier(w *daemonWiring) plugins.IndexVerifier {
	return func(ctx context.Context, index []byte) (plugin.RegistryIndex, error) {
		cfg := plugin.RegistryConfig{PublicKey: pluginReverifyKey(w)}
		if w.Paths != nil {
			cfg.CacheDir = filepath.Join(w.Paths.DataDir(), "plugin-registry-cache")
		}
		if w.Deps.Config != nil {
			cfg.RegistryURL = w.Deps.Config.Registry.URL
			if w.Deps.Config.Registry.CacheDir != "" {
				cfg.CacheDir = w.Deps.Config.Registry.CacheDir
			}
		}
		clock := w.Clock
		c := plugin.NewRegistryClient(cfg, indexBytesFetcher{index: index},
			plugin.Ed25519Verifier{PublicKey: cfg.PublicKey}, nil, clock)
		return c.Fetch(ctx)
	}
}
