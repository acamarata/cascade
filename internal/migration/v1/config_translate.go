// Package v1 uses this file for pure, deterministic config key translation.
// Purpose: translate closed v1 config keys without I/O.
// Inputs: raw v1 TOML bytes.
// Outputs: validated known-key mappings plus a sorted unknown-key quarantine.
// Constraints: schema 0/1 only; no catch-all mapping or lossy coercion.
// SPORT: migration/v1/config/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

type configMapping struct {
	Source  string
	Target  string
	Value   interface{}
	Literal string
}

func translateV1Config(data []byte) ([]configMapping, []string, error) {
	tree := map[string]interface{}{}
	if err := toml.Unmarshal(data, &tree); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindIntegrity, err,
			"migration v1 config: malformed TOML")
	}
	flat := map[string]interface{}{}
	flattenConfig(tree, "", flat)
	if _, err := configSchemaVersion(flat); err != nil {
		return nil, nil, err
	}
	delete(flat, "schema_version")
	mapped := []configMapping{{Source: "schema_version", Target: "schema_version",
		Value: int64(runtime.CurrentSchemaVersion), Literal: strconv.Itoa(runtime.CurrentSchemaVersion)}}
	unknown := make([]string, 0)
	keys := sortedMapKeys(flat)
	for _, key := range keys {
		item, ok, mapErr := mapConfigValue(key, flat[key])
		if mapErr != nil {
			return nil, nil, mapErr
		}
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		mapped = append(mapped, item)
	}
	return mapped, unknown, nil
}

func configSchemaVersion(flat map[string]interface{}) (int64, error) {
	raw, ok := flat["schema_version"]
	if !ok {
		return 0, nil
	}
	version, ok := raw.(int64)
	if !ok {
		return 0, cascade.Wrap(cascade.KindIntegrity, ErrMalformedInput,
			"migration v1 config: schema_version is not an integer")
	}
	if version != 0 && version != 1 {
		return 0, cascade.Wrapf(cascade.KindUnsupported, ErrVersionMismatch,
			"migration v1 config: schema_version %d is not supported", version)
	}
	return version, nil
}

func mapConfigValue(key string, value interface{}) (configMapping, bool, error) {
	switch key {
	case "daemon.log_level":
		return mapLogLevel(key, value)
	case "daemon.log_format":
		return mapLogFormat(key, value)
	case "daemon.socket_path":
		return mapStringValue(key, "daemon.socket", value)
	case "telemetry.enabled":
		return mapBoolValue(key, "telemetry.enabled", value)
	default:
		return configMapping{}, false, nil
	}
}

func mapLogLevel(key string, value interface{}) (configMapping, bool, error) {
	level, ok := value.(string)
	if !ok {
		return configMapping{}, false, malformedConfigType(key, "string")
	}
	switch level {
	case "debug", "info", "warn", "error":
		return stringMapping(key, "logging.level", level), true, nil
	default:
		return configMapping{}, false, unknownConfigValue(key, level)
	}
}

func mapLogFormat(key string, value interface{}) (configMapping, bool, error) {
	format, ok := value.(string)
	if !ok {
		return configMapping{}, false, malformedConfigType(key, "string")
	}
	switch format {
	case "json":
		return stringMapping(key, "logging.format", format), true, nil
	case "pretty", "text":
		return stringMapping(key, "logging.format", "text"), true, nil
	default:
		return configMapping{}, false, unknownConfigValue(key, format)
	}
}

func mapStringValue(source, target string, value interface{}) (configMapping, bool, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return configMapping{}, false, malformedConfigType(source, "non-empty string")
	}
	return stringMapping(source, target, text), true, nil
}

func mapBoolValue(source, target string, value interface{}) (configMapping, bool, error) {
	flag, ok := value.(bool)
	if !ok {
		return configMapping{}, false, malformedConfigType(source, "boolean")
	}
	return configMapping{Source: source, Target: target, Value: flag,
		Literal: strconv.FormatBool(flag)}, true, nil
}

func stringMapping(source, target, value string) configMapping {
	return configMapping{Source: source, Target: target, Value: value,
		Literal: strconv.Quote(value)}
}

func malformedConfigType(key, expected string) error {
	return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedInput,
		"migration v1 config: %s must be a %s", key, expected)
}

func unknownConfigValue(key, value string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
		"migration v1 config: %s has unmappable value %q", key, value)
}

func flattenConfig(tree map[string]interface{}, prefix string, out map[string]interface{}) {
	for key, value := range tree {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(map[string]interface{}); ok {
			if len(nested) == 0 {
				out[path] = struct{}{}
				continue
			}
			flattenConfig(nested, path, out)
			continue
		}
		out[path] = value
	}
}

func sortedMapKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func dottedValue(tree map[string]interface{}, dotted string) (interface{}, bool) {
	current := tree
	segments := strings.Split(dotted, ".")
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]interface{})
		if !ok {
			return nil, false
		}
		current = next
	}
	value, ok := current[segments[len(segments)-1]]
	return value, ok
}

func setDottedValue(tree map[string]interface{}, dotted string, value interface{}) {
	current := tree
	segments := strings.Split(dotted, ".")
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			current[segment] = next
		}
		current = next
	}
	current[segments[len(segments)-1]] = value
}
