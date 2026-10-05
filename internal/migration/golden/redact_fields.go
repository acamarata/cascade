// Purpose: the field-level halves of the allowlist Redactor
// (redact_allowlist.go): the config key sets, the journal code set, and the
// per-domain Source / Target / ContentHash / config-value rules.
// Inputs: one importer value and the redactor's domain and input path.
// Outputs: the kept value or its synthetic / REDACTED replacement.
// Constraints: allowlist only. Split from redact_allowlist.go for the
// 300-line file cap; it adds no rule of its own.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"strings"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
)

// configKeyKeep is the v2 key set the v1 config translator maps to
// (internal/migration/v1/config_translate.go mapConfigValue and the
// schema_version row). TestRedactAllowlist_ConfigKeysMatchTranslator runs
// the real importer and proves this set equals what it writes.
var configKeyKeep = map[string]bool{
	"schema_version": true, "logging.level": true, "logging.format": true,
	"daemon.socket": true, "telemetry.enabled": true,
}

// configSourceKeep is the v1 key set that translator reads.
var configSourceKeep = map[string]bool{
	"schema_version": true, "daemon.log_level": true, "daemon.log_format": true,
	"daemon.socket_path": true, "telemetry.enabled": true,
}

// journalCodeKeep are the importer journal codes kept verbatim.
var journalCodeKeep = map[string]bool{
	"accounts.metadata": true, "accounts.model-matrix": true,
	"memory.provenance": true, "v1_unknown_config": true,
}

// pathMarkers are the characters that make a config value path-shaped on any
// supported platform: both separators, the home tilde, a Windows %VAR% or a
// shell $VAR expansion, and a drive or scheme colon.
const pathMarkers = `/\~%$:`

// pathLike reports whether a config value names a location rather than a bare
// file name. daemon.socket is the only free-form allowlisted value, so any
// marker redacts it: only a bare file name survives.
func pathLike(text string) bool { return strings.ContainsAny(text, pathMarkers) }

// source replaces an importer's logical source with the input-relative path
// and keeps a "#fragment" only when the domain's rule classifies it.
func (r *redactor) source(raw string) string {
	_, fragment, hasFragment := strings.Cut(raw, "#")
	if !hasFragment {
		return r.inputRel
	}
	if r.domain == v1.DomainConfig && configSourceKeep[fragment] {
		return r.inputRel + "#" + fragment
	}
	return r.inputRel + "#" + r.mapped(fragment)
}

// target maps a Change target per domain.
func (r *redactor) target(raw string) string {
	switch r.domain {
	case v1.DomainMemory:
		return raw
	case v1.DomainVault:
		return r.name(raw)
	case v1.DomainAccounts:
		return r.account(raw)
	case v1.DomainConfig:
		if configKeyKeep[raw] {
			return raw
		}
	}
	return redacted
}

// contentHash keeps a memory body hash (the body itself is kept) and
// REDACTs every other domain's hash: a digest of a short value is a
// fingerprint that can be brute-forced.
func (r *redactor) contentHash(raw string) string {
	if r.domain == v1.DomainMemory {
		return raw
	}
	return redacted
}

// configKeys keeps the translator's v2 keys (path-shaped values aside) and
// REDACTs every other key's value, never dropping the key.
func (r *redactor) configKeys(flat map[string]any) map[string]any {
	out := make(map[string]any, len(flat))
	for key, value := range flat {
		out[key] = redacted
		if !configKeyKeep[key] {
			continue
		}
		out[key] = value
		if text, ok := value.(string); ok && pathLike(text) {
			out[key] = redactedPath
		}
	}
	return out
}
