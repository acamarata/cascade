// Package v1 uses this file to translate verified .cascade/config.toml data.
// Purpose: translate the verified v1 .cascade/config.toml format into v2.
// Inputs: one v1 config file and the injected v2 config destination path.
// Outputs: a validated, atomic config update or an exact dry-run delta.
// Constraints: unknown or unmappable keys refuse the whole import; existing v2
// values win visibly; the candidate passes runtime.Validate before one write.
// SPORT: migration/v1/config/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const maxConfigBytes = 16 << 20

type configImporter struct{ destination string }

// NewConfigImporter returns the config importer through the sole Importer
// interface. destination is the already-resolved v2 config.toml path.
func NewConfigImporter(destination string) Importer {
	return &configImporter{destination: destination}
}

func (c *configImporter) Import(_ context.Context, req Request) (DryRunResult, error) {
	result := DryRunResult{Domain: DomainConfig}
	if strings.TrimSpace(c.destination) == "" {
		return result.Normalize(), cascade.New(cascade.KindInvalidInput,
			"migration v1 config: destination path is required")
	}
	source, sourceName, err := readV1Config(req.SourceRoot)
	if err != nil {
		return result.Normalize(), err
	}
	mapped, unknown, err := translateV1Config(source)
	if err != nil {
		return result.Normalize(), err
	}
	appendUnknownConfig(&result, sourceName, unknown)
	if len(unknown) > 0 {
		return result.Normalize(), cascade.Wrap(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 config: unknown keys quarantined; destination was not written")
	}
	planned := result
	candidate, err := c.buildCandidate(mapped, sourceName, &planned)
	if err != nil {
		return result.Normalize(), err
	}
	result = planned
	if req.DryRun || result.EmptyDelta() {
		return result.Normalize(), nil
	}
	if err := runtime.WriteBytesAtomic(c.destination, candidate); err != nil {
		return result.Normalize(), cascade.Wrap(cascade.KindUnavailable, err,
			"migration v1 config: atomically write validated destination")
	}
	result.Applied = true
	return result.Normalize(), nil
}

func readV1Config(root string) ([]byte, string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, "", cascade.New(cascade.KindInvalidInput,
			"migration v1 config: source root is required")
	}
	path, err := oneRegularSource([]string{filepath.Join(root, ".cascade", "config.toml")}, "config")
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "migration v1 config: read source")
	}
	if len(data) > maxConfigBytes {
		return nil, "", cascade.Wrap(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 config: source exceeds the size limit")
	}
	return data, ".cascade/config.toml", nil
}

func (c *configImporter) buildCandidate(mapped []configMapping, source string, result *DryRunResult) ([]byte, error) {
	current, tree, err := readV2Config(c.destination)
	if err != nil {
		return nil, err
	}
	candidate := current
	for _, item := range mapped {
		candidate, err = mergeConfigItem(candidate, tree, item, source, result)
		if err != nil {
			return nil, err
		}
	}
	if err := validateConfigCandidate(candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}

func readV2Config(path string) ([]byte, map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err,
			"migration v1 config: read destination")
	}
	tree := map[string]interface{}{}
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &tree); err != nil {
			return nil, nil, cascade.Wrap(cascade.KindIntegrity, err,
				"migration v1 config: destination is malformed")
		}
	}
	return data, tree, nil
}

func mergeConfigItem(data []byte, tree map[string]interface{}, item configMapping, source string, result *DryRunResult) ([]byte, error) {
	existing, found := dottedValue(tree, item.Target)
	if found {
		if reflect.DeepEqual(existing, item.Value) {
			result.Changes = append(result.Changes, configChange(OperationUnchanged, source, item))
			return data, nil
		}
		result.Changes = append(result.Changes, configChange(OperationSkip, source, item))
		return nil, cascade.Wrapf(cascade.KindConflict, ErrImportConflict,
			"migration v1 config: destination key %q already has a different value", item.Target)
	}
	updated, err := runtime.SetKeyLine(data, item.Target, item.Literal)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err,
			"migration v1 config: rejected v2 key %q", item.Target)
	}
	setDottedValue(tree, item.Target, item.Value)
	result.Changes = append(result.Changes, configChange(OperationCreate, source, item))
	return updated, nil
}

func validateConfigCandidate(data []byte) error {
	tree := map[string]interface{}{}
	if err := toml.Unmarshal(data, &tree); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err,
			"migration v1 config: translated candidate is malformed")
	}
	if err := runtime.Validate(tree); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err,
			"migration v1 config: translated candidate failed v2 validation")
	}
	return nil
}

func configChange(operation Operation, source string, item configMapping) Change {
	return Change{Operation: operation, Source: source + "#" + item.Source,
		Target: item.Target, ContentHash: digestBytes([]byte(item.Literal))}
}

func appendUnknownConfig(result *DryRunResult, source string, keys []string) {
	for _, key := range keys {
		result.Journal = append(result.Journal, JournalEntry{Code: "v1_unknown_config",
			Source: source + "#" + key,
			Detail: "unmapped v1 key quarantined; no destination field was written"})
	}
}
