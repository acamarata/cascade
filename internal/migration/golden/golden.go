// Purpose: the harvester's two construction seams: which real v1 importer
// runs for a domain, and which custody configuration the vault harvest's
// broker is built from.
// Inputs: a domain plus the scratch-only destination handles (memory
// FileStore, vault Broker, provider Registry, v2 config path).
// Outputs: the unmodified internal/migration/v1 importers, and a
// secrets.Config pinned to the encrypted file vault under scratch.
// Constraints: the importers are called exactly as `cascade migrate v1`
// calls them; nothing here re-implements import logic. The harvest custody
// never names the production vault service and always forces the file
// vault, so no platform keychain or secret service is ever probed.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"path/filepath"

	"github.com/acamarata/cascade/internal/memory"
	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
)

// harvestService is the only custody service label a harvest may use.
const harvestService = "cascade-golden-harvest"

// importerDeps are the scratch destinations one domain's importer writes.
type importerDeps struct {
	store      *memory.FileStore
	broker     *secrets.Broker
	registry   *registry.Registry
	configDest string
}

// importerFor returns the importer for a domain. It is a package var only so
// tests can inject an importer failure; Run never replaces it.
var importerFor = realImporter

// realImporter builds the real v1 importer for domain over deps.
func realImporter(domain v1.Domain, deps importerDeps) v1.Importer {
	switch domain {
	case v1.DomainMemory:
		return v1.NewMemoryImporter(deps.store)
	case v1.DomainVault:
		return v1.NewVaultImporter(deps.broker)
	case v1.DomainAccounts:
		return v1.NewAccountsImporter(deps.registry)
	case v1.DomainConfig:
		return v1.NewConfigImporter(deps.configDest)
	}
	return nil
}

// custodyConfigFor is the single place the harvest obtains its custody
// configuration (tests wrap it to observe the config Run actually uses).
var custodyConfigFor = harvestCustodyConfig

// harvestCustodyConfig returns the custody configuration for a vault
// harvest rooted at scratch: the dedicated harvest service label, a file
// vault directory inside scratch, and ForceFileVault so SelectCustody never
// reaches a platform backend.
func harvestCustodyConfig(scratch string) secrets.Config {
	return secrets.Config{
		Service:        harvestService,
		Dir:            filepath.Join(scratch, "custody"),
		ForceFileVault: true,
	}
}
