// Purpose: harvest the config domain, one scratch v1 home per input, and
// record the translator's designed refusal as a refusal fixture.
// Inputs: one verified config input.
// Outputs: either a config fixture (the v2 config.toml the importer wrote,
// parsed to a redacted dotted-key map, plus its redacted Changes) or a
// config-refusal fixture {input, error, refused_key_paths}.
// Constraints: a refusal is defined exactly (R7b N-4): err != nil AND the
// journal holds >= 1 entry with Code "v1_unknown_config". The error's Kind
// is never consulted (a cascade sentinel match compares Kind only). Any other
// error refuses the run.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

// unknownConfigCode is the journal code the config importer writes for each
// quarantined v1 key (internal/migration/v1/config.go appendUnknownConfig).
const unknownConfigCode = "v1_unknown_config"

// knownRefusalMessage is the one refusal error text a fixture may carry;
// any other text is REDACTED.
const knownRefusalMessage = "invalid-input: migration v1 config: unknown keys quarantined; " +
	"destination was not written: invalid-input: migration v1: unknown input"

// harvestConfig returns the fixture for one config input.
func harvestConfig(ctx context.Context, scratch string, file inputFile) (fixture, error) {
	home := filepath.Join(scratch, "home")
	if err := stage(home, ".cascade/config.toml", file.Data); err != nil {
		return fixture{}, err
	}
	dest := filepath.Join(scratch, "v2", "config.toml")
	importer := importerFor(v1.DomainConfig, importerDeps{configDest: dest})
	if importer == nil {
		return fixture{}, cascade.New(cascade.KindInternal, "golden harvest: no config importer")
	}
	res, err := importer.Import(ctx, v1.Request{SourceRoot: home})
	if err != nil {
		refused := refusedConfigKeys(res)
		if len(refused) == 0 {
			return fixture{}, &importRefusal{domain: v1.DomainConfig, input: file.Rel, cause: err}
		}
		return configRefusalFixture(file, err, refused)
	}
	flat, err := readV2ConfigKeys(dest)
	if err != nil {
		return fixture{}, err
	}
	r := newRedactor(v1.DomainConfig, file.Rel)
	record := r.dryRun(res)
	record["config"] = r.configKeys(flat)
	return newFixture(v1.DomainConfig, "config", file, record)
}

// refusedConfigKeys returns the v1 key paths of the journal's
// v1_unknown_config entries (each entry's Source after "#"), sorted.
func refusedConfigKeys(res v1.DryRunResult) []string {
	var keys []string
	for _, entry := range res.Journal {
		if entry.Code != unknownConfigCode {
			continue
		}
		if _, key, ok := strings.Cut(entry.Source, "#"); ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// configRefusalFixture records a translator refusal. The error text is kept
// only when it is the importer's known fixed refusal message.
func configRefusalFixture(file inputFile, err error, refused []string) (fixture, error) {
	message := redacted
	if err.Error() == knownRefusalMessage {
		message = knownRefusalMessage
	}
	record := map[string]any{"error": message, "refused_key_paths": refused}
	return newFixture(v1.DomainConfig, "config-refusal", file, record)
}

// readV2ConfigKeys parses the v2 config the importer wrote into a dotted-key
// map. A missing file (nothing to write) is an empty map.
func readV2ConfigKeys(dest string) (map[string]any, error) {
	data, err := os.ReadFile(dest) //nolint:gosec // scratch destination built above
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: read back the v2 config")
	}
	tree := map[string]any{}
	if err := toml.Unmarshal(data, &tree); err != nil {
		return nil, cascade.New(cascade.KindIntegrity, "golden harvest: the v2 config the importer wrote is malformed")
	}
	flat := map[string]any{}
	flatten(tree, "", flat)
	return flat, nil
}

// flatten writes every leaf of tree into out under its dotted path.
func flatten(tree map[string]any, prefix string, out map[string]any) {
	for key, value := range tree {
		dotted := key
		if prefix != "" {
			dotted = prefix + "." + key
		}
		if nested, ok := value.(map[string]any); ok {
			flatten(nested, dotted, out)
			continue
		}
		out[dotted] = value
	}
}

// harvestConfigInputs harvests each config input in its own scratch home,
// never two in one run of the importer.
func harvestConfigInputs(ctx context.Context, files []inputFile) ([]fixture, error) {
	out := make([]fixture, 0, len(files))
	for _, file := range files {
		fx, err := withScratch(func(scratch string) (fixture, error) {
			return harvestConfig(ctx, scratch, file)
		})
		if err != nil {
			return nil, err
		}
		out = append(out, fx)
	}
	return out, nil
}
