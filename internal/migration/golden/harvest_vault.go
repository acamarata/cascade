// Purpose: harvest the vault domain without any real v1 key name or value
// ever reaching a custody.
// Inputs: one verified vault input and the custody Config to build the
// scratch broker from (custodyConfigFor(scratch) in a run).
// Outputs: one fixture holding the importer's Changes, redacted: operation
// kept, target -> NAME-<n>, source -> the input-relative path, content_hash
// REDACTED.
// Constraints: the input is parsed, every entry renamed to
// MIG_FIXTURE_SECRET_<n> (1-based over the sorted original names) and every
// value replaced by NONSECRET-<relpath-slug>-<n> in memory before a byte is
// staged; the broker must answer as the encrypted file vault or the harvest
// refuses. The committed REDACTED sentinel is refused by the importer by
// design, which is why the synthetic value exists.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fileVaultBackend is the Broker.Backend() name of the encrypted file vault.
const fileVaultBackend = "file-vault"

// fixtureSecretPrefix is the synthetic vault entry name prefix.
const fixtureSecretPrefix = "MIG_FIXTURE_SECRET_"

// slugUnsafe matches every run of bytes a relpath slug may not hold.
var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// harvestVault returns the vault fixture for one input, importing into a
// broker built from cfg under a scratch v1 home inside scratch.
func harvestVault(ctx context.Context, scratch string, file inputFile, cfg secrets.Config) (fixture, error) {
	synthetic, err := renameVaultEntries(file)
	if err != nil {
		return fixture{}, err
	}
	home := filepath.Join(scratch, "home")
	if err := stage(home, ".claude/vault.env", synthetic); err != nil {
		return fixture{}, err
	}
	custody, err := secrets.SelectCustody(cfg)
	if err != nil {
		return fixture{}, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: select the harvest custody")
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return fixture{}, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: build the harvest broker")
	}
	if broker.Backend() != fileVaultBackend {
		return fixture{}, cascade.New(cascade.KindIntegrity, "golden harvest: the harvest custody is not the encrypted file vault")
	}
	res, err := runImporter(ctx, importerFor(v1.DomainVault, importerDeps{broker: broker}), v1.DomainVault, file.Rel, home)
	if err != nil {
		return fixture{}, err
	}
	r := newRedactor(v1.DomainVault, file.Rel)
	return newFixture(v1.DomainVault, "vault", file, r.dryRun(res))
}

// renameVaultEntries rewrites a vault.env so that no original name or value
// survives: names become MIG_FIXTURE_SECRET_<n>, values NONSECRET-<slug>-<n>.
// Every value is replaced, REDACTED or not, so a real value in a pinned
// input could still never reach a custody.
func renameVaultEntries(file inputFile) ([]byte, error) {
	entries, err := secrets.ParseVaultEnv(file.Data)
	if err != nil {
		// The parser's own message is not carried: a name it rejects could
		// be quoted in it.
		return nil, cascade.Newf(cascade.KindIntegrity, "golden harvest: %s is not a parseable vault.env", file.Rel)
	}
	unique := map[string]bool{}
	for _, entry := range entries {
		unique[entry.Name] = true
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)
	index := make(map[string]int, len(names))
	for i, name := range names {
		index[name] = i + 1
	}
	slug := strings.Trim(slugUnsafe.ReplaceAllString(file.Rel, "-"), "-")
	var b strings.Builder
	for _, entry := range entries {
		n := strconv.Itoa(index[entry.Name])
		b.WriteString(fixtureSecretPrefix + n + "=NONSECRET-" + slug + "-" + n + "\n")
	}
	return []byte(b.String()), nil
}

// harvestVaultInputs harvests each vault input in its own scratch home with
// the custody config custodyConfigFor returns for that scratch.
func harvestVaultInputs(ctx context.Context, files []inputFile) ([]fixture, error) {
	out := make([]fixture, 0, len(files))
	for _, file := range files {
		fx, err := withScratch(func(scratch string) (fixture, error) {
			return harvestVault(ctx, scratch, file, custodyConfigFor(scratch))
		})
		if err != nil {
			return nil, err
		}
		out = append(out, fx)
	}
	return out, nil
}

// withScratch runs fn in a fresh scratch dir and removes it afterwards.
func withScratch(fn func(scratch string) (fixture, error)) (fixture, error) {
	scratch, err := newScratch()
	if err != nil {
		return fixture{}, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	return fn(scratch)
}
