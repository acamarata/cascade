package runtime

// Purpose: the writing side of the config baseline: the one extraction of
//
//	the guarded values (EffectiveConfigOf), writing a chosen EffectiveConfig
//	back into a *Config (ApplyEffective), and persisting a signed record for
//	an accepted looser config (Accept).
//
// Inputs: a *Config, an EffectiveConfig, a BaselineProof.
// Outputs: a Config whose guarded keys equal the EffectiveConfig; the v2
//
//	record and its snapshot in the audit namespace.
//
// Constraints: ApplyEffective writes only the modeled keys and copies every
//
//	map it changes (cfg.Extra and each touched section), so a map another
//	holder shares is never mutated. Accept verifies the proof before it
//	writes, writes the snapshot, the audit record and last the v2 record
//	(the commit point), and returns the first failure as KindUnavailable;
//	nil means all three persisted.
//
// SPORT: runtime/boot-baseline-check (accept + apply).

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/pkg/cascade"
)

// auditKindBaselineAccepted is the record kind written when a looser
// config is accepted through config.accept_loosening.
const auditKindBaselineAccepted = "baseline_accepted"

// eventBaselineAccepted is published once an acceptance committed.
const eventBaselineAccepted = "config.baseline.accepted"

// LoosenedSinceBoot lists, sorted, every guarded key accepted loosens
// relative to the values this process booted with (the last CheckFile's
// Effective). Guarded consumers read their values at start, so these keys
// take effect at the next start, however many acceptances ran since boot.
func (b *BaselineChecker) LoosenedSinceBoot(accepted EffectiveConfig) []string {
	boot := MostRestrictiveDefaults()
	if b.boot != nil {
		boot = *b.boot
	}
	keys := []string{}
	for _, p := range CompareSecurity(boot, accepted) {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

// EffectiveConfigOf returns cfg's guarded values. It is the one extraction
// every caller uses.
func EffectiveConfigOf(cfg *Config) EffectiveConfig { return extractEffectiveConfig(cfg) }

// ApplyEffective writes eff's guarded values into cfg: the modeled keys of
// [policy], [secrets], [sync], [nodes] and [conductor] in cfg.Extra, and
// cfg.Elevation. Unmodeled keys are untouched. A string value of "" removes
// its key (absent and empty read the same); booleans are always written,
// so an absent key can never fall back to a looser consumer default.
func ApplyEffective(cfg *Config, eff EffectiveConfig) {
	if cfg == nil {
		return
	}
	extra := make(map[string]interface{}, len(cfg.Extra)+5)
	for k, v := range cfg.Extra {
		extra[k] = v
	}
	setStringKey(extra, "policy", "autonomy_profile", eff.Policy.AutonomyProfile)
	setStringKey(extra, "secrets", "keychain_backend", eff.Secrets.KeychainBackend)
	setStringKey(extra, "nodes", "trust_tier", eff.Nodes.TrustTier)
	conductor := cloneSection(extra, "conductor")
	conductor["external_routing_enabled"] = eff.Conductor.ExternalRoutingEnabled
	conductor["spill_enabled"] = eff.Conductor.SpillEnabled
	extra["conductor"] = conductor
	applySyncClasses(extra, eff.Sync)
	cfg.Extra = extra
	cfg.Elevation = eff.Elevation
}

// cloneSection returns a copy of extra[section] as a table (empty when
// absent or not a table).
func cloneSection(extra map[string]interface{}, section string) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range sectionAt(extra, section) {
		out[k] = v
	}
	return out
}

// setStringKey writes value at section.key, removing the key for "" and
// leaving an untouched section absent when there is nothing to write.
func setStringKey(extra map[string]interface{}, section, key, value string) {
	if sectionAt(extra, section) == nil && value == "" {
		return
	}
	sec := cloneSection(extra, section)
	if value == "" {
		delete(sec, key)
	} else {
		sec[key] = value
	}
	extra[section] = sec
}

// applySyncClasses makes [sync]'s string-valued keys exactly s.Classes;
// keys of any other type are not domain classes and are kept.
func applySyncClasses(extra map[string]interface{}, s SyncSection) {
	if sectionAt(extra, "sync") == nil && len(s.Classes) == 0 {
		return
	}
	sec := cloneSection(extra, "sync")
	for k, v := range sec {
		if _, isString := v.(string); isString {
			delete(sec, k)
		}
	}
	for domain, class := range s.Classes {
		sec[domain] = class
	}
	extra["sync"] = sec
}

// Accept persists onDisk as the baseline, signed by proof: the snapshot,
// then the audit record, then the v2 record (the commit point). The proof must cover onDisk's file (its params'
// digest) and verify; otherwise nothing is written. A store failure returns
// KindUnavailable wrapping the first error.
func (b *BaselineChecker) Accept(ctx context.Context, onDisk EffectiveConfig, proof BaselineProof) error {
	digest, ok := DecodeAcceptParams(proof.RawParams)
	if !ok {
		return cascade.New(cascade.KindInvalidInput, "baseline: accept params are not {\"config_sha256\": <digest>}")
	}
	if err := b.proofCovers(proof, digest, digest); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "baseline: accept proof refused")
	}
	if b.store == nil {
		return cascade.New(cascade.KindUnavailable, "baseline: no store to record the accepted config in")
	}
	hash := ComputeSectionsHash(onDisk)
	rec := baselineRecordV1{
		Version: baselineRecordVersionSigned, SectionsHash: hash, Sections: baselineGuardedSections, ConfigSHA256: digest,
		Proof: &baselineProofDoc{Method: proof.Method, RawParams: proof.RawParams, Attestation: proof.Attestation},
	}
	recData, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: encode record")
	}
	snapData, err := json.Marshal(onDisk)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: encode snapshot")
	}
	// The snapshot and the audit record go first; the v2 record is the
	// commit point and is written last, so any earlier failure leaves the
	// previous baseline in force.
	if err := b.store.Put(ctx, auditNamespace, baselineSnapshotKey, snapData); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: write snapshot")
	}
	if b.audit == nil {
		return cascade.New(cascade.KindUnavailable, "baseline: no audit recorder; an acceptance is never left unaudited")
	}
	// The audit and event field restart_required_keys holds only the guarded
	// keys loosened since boot; the RPC result's field of the same name also
	// carries the reload's own cold-section restart keys.
	restart := b.LoosenedSinceBoot(onDisk)
	fields := map[string]interface{}{"sections_hash": hash, "config_sha256": digest, "restart_required_keys": restart}
	if err := b.audit.Record(ctx, auditKindBaselineAccepted, fields); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: write audit record")
	}
	if err := b.store.Put(ctx, auditNamespace, baselineRecordKey, recData); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: write record")
	}
	b.events.Publish(ctx, eventBaselineAccepted, fields)
	return nil
}

// restrictiveAutonomyProfile and restrictiveSyncClass are the most
// restrictive explicit values of their keys. Both are written explicitly:
// an absent policy.autonomy_profile resolves to "balanced" and an absent
// [sync] domain to its compiled class, both looser.
const (
	restrictiveAutonomyProfile = "strict"
	restrictiveSyncClass       = "local-only"
)

// RestrictiveFallback is what a degraded boot runs instead of onDisk: the
// most-restrictive defaults, with policy.autonomy_profile pinned to
// "strict" and every [sync] domain onDisk names pinned to "local-only".
// No guarded key ends up looser than onDisk's value, including under its
// consumer's absent-key default. Domains onDisk does not name keep their
// compiled class, exactly as onDisk would run them.
func RestrictiveFallback(onDisk EffectiveConfig) EffectiveConfig {
	eff := MostRestrictiveDefaults()
	eff.Policy.AutonomyProfile = restrictiveAutonomyProfile
	for domain := range onDisk.Sync.Classes {
		eff.Sync.Classes[domain] = restrictiveSyncClass
	}
	return eff
}

// ConfigFile is one read of config.toml: its exact bytes, read once with
// Load's permission and ownership checks.
type ConfigFile struct {
	Path  string
	Data  []byte
	Found bool
}

// ReadConfigFile reads path once with Load's permission and ownership
// checks (a world-writable or foreign-owned file is refused).
func ReadConfigFile(path string) (ConfigFile, error) {
	_, _, warn := loadOptionDefaults(LoadOptions{})
	data, found, err := readConfigChecked(path, warn)
	if err != nil {
		return ConfigFile{}, err
	}
	return ConfigFile{Path: path, Data: data, Found: found}, nil
}

// Digest is ConfigDigest of the bytes that were read.
func (f ConfigFile) Digest() string { return ConfigDigest(f.Data) }

// Parse builds the Config these exact bytes declare, with opts' environment
// and profile flag (opts.Path is ignored), the way Load does, without ever
// reopening the file: what is parsed is what was hashed.
func (f ConfigFile) Parse(ctx context.Context, opts LoadOptions) (*Config, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	getenv, environ, warn := loadOptionDefaults(opts)
	tree := map[string]interface{}{}
	sources := map[string]ConfigSource{}
	if !f.Found {
		tree["schema_version"] = int64(CurrentSchemaVersion)
	} else {
		if err := toml.Unmarshal(f.Data, &tree); err != nil {
			return nil, &ConfigError{Reason: fmt.Sprintf("malformed TOML in %s: %v", f.Path, err)}
		}
		markSources(tree, "", sources, SourceFile)
		if _, err := UpgradeSchema(tree); err != nil {
			return nil, err
		}
	}
	for k, v := range collectEnvOverrides(environ(), warn) {
		treeSet(tree, k, v)
		sources[k] = SourceEnv
	}
	sec, err := parseConfigSections(tree, warn)
	if err != nil {
		return nil, err
	}
	profile, profSource, err := resolveRuntimeSection(tree, opts.ProfileFlag, getenv, warn)
	if err != nil {
		return nil, err
	}
	sources["runtime.profile"] = profSource
	return assembleConfig(sec, schemaVersionOf(tree), profile, tree, sources), nil
}
