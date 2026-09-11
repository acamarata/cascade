// Purpose: S-42.T1's target registry -- persisted TargetRecord definitions
// (name, kind fs|s3|rclone, driver config) and BuildTarget, the constructor
// that turns one record into S-41.T3's real driver by kind. This is the
// data model behind S-42.T3's `backup target add|list|remove`, which is out
// of this ticket's scope.
//
// Inputs: a provider.Store namespace (caller-supplied) and TargetRecord
// values.
// Outputs: persisted records, or a real backup.Target driver.
// Constraints: creds resolve via vault/env-ref only (08-INIT-CONFIG-SPEC.md
// §2; §D-15) -- a record never carries a literal credential. Every kind-
// specific field is checked against the H/S-15.T3 detector at its default,
// precision-first threshold (looksLikeSecret, mirroring
// internal/providers/intake/config.go's own use of the same check) before
// it is ever persisted, so a secret-looking literal is refused at
// registration time, not discovered later.
//
// SPORT: internal.backup.targets/ADD (P1-E19-W4-S42-T1).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TargetKind identifies which of S-41.T3's drivers a TargetRecord builds.
// The set is exactly the three drivers that ticket shipped: fs, s3, rclone.
type TargetKind string

// The closed three-member target-kind enumeration.
const (
	TargetKindFS     TargetKind = "fs"
	TargetKindS3     TargetKind = "s3"
	TargetKindRclone TargetKind = "rclone"
)

// TargetRecord is one persisted backup target: a name, its driver kind, and
// the kind-specific, NEVER-secret driver config. Exactly one of the three
// kind-specific fields is meaningful, per Kind: FSRoot (fs, a local path),
// S3EnvPrefix (s3, the env-ref prefix ResolveS3TargetEnvRefs expands --
// never the credentials themselves), or RcloneRemote (rclone, an rclone
// remote spec or bare local path -- rclone resolves its own credentials
// from the operator's rclone config, outside this record entirely).
type TargetRecord struct {
	Name         string
	Kind         TargetKind
	FSRoot       string
	S3EnvPrefix  string
	RcloneRemote string
}

// targetKeyPrefix namespaces this file's persisted records apart from
// policy.go's and schedule.go's own prefixes sharing the same Store
// namespace.
const targetKeyPrefix = "backup:target:"

func targetKey(name string) string { return targetKeyPrefix + name }

// Validate fails closed on a record this build cannot safely persist: an
// empty name, an unknown kind, a missing kind-specific field, or a kind-
// specific field that looks like a literal credential (H/S-15.T3).
func (r TargetRecord) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return cascade.New(cascade.KindInvalidInput, "backup: target name is required")
	}
	switch r.Kind {
	case TargetKindFS:
		return validateTargetField("fs target root", r.FSRoot)
	case TargetKindS3:
		return validateTargetField("s3 target env-ref prefix", r.S3EnvPrefix)
	case TargetKindRclone:
		return validateTargetField("rclone target remote", r.RcloneRemote)
	default:
		return cascade.Newf(cascade.KindInvalidInput, "backup: unknown target kind %q", r.Kind)
	}
}

// validateTargetField refuses an empty value and a secret-looking one.
func validateTargetField(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return cascade.Newf(cascade.KindInvalidInput, "backup: target requires a %s", label)
	}
	if looksLikeSecret(value) {
		return cascade.Newf(cascade.KindInvalidInput,
			"backup: target %s must not be a literal credential; use a vault/env-ref name instead", label)
	}
	return nil
}

// looksLikeSecret reports whether value would be flagged as credential
// material by the H/S-15.T3 detector at its default, precision-first
// threshold -- the same check internal/providers/intake/config.go's
// looksLikeSecret uses, reused here so a target's driver config cannot
// smuggle a literal past the one detector this repo trusts to make that
// call.
func looksLikeSecret(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		// Fail closed: a detector this build cannot construct is not
		// "assume safe", it is "assume the worst" -- a nil-detector
		// bypass would be silent.
		return true
	}
	return len(detector.ScanCertain([]byte(value))) > 0
}

// PutTarget upserts rec's record in namespace, after Validate.
func PutTarget(ctx context.Context, store provider.Store, namespace string, rec TargetRecord) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	data, err := encodeTargetRecord(rec)
	if err != nil {
		return err
	}
	if err := store.Put(ctx, namespace, targetKey(rec.Name), data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: persist target record")
	}
	return nil
}

// GetTarget reads one persisted TargetRecord by name, or a KindNotFound
// error if absent.
func GetTarget(ctx context.Context, store provider.Store, namespace, name string) (TargetRecord, error) {
	data, err := store.Get(ctx, namespace, targetKey(name))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return TargetRecord{}, err
		}
		return TargetRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "backup: read target record")
	}
	return decodeTargetRecord(data)
}

// DeleteTarget removes name's persisted record. Deleting an absent target
// is not an error, matching provider.Store.Delete's own idempotence.
func DeleteTarget(ctx context.Context, store provider.Store, namespace, name string) error {
	if err := store.Delete(ctx, namespace, targetKey(name)); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: delete target record")
	}
	return nil
}

// ListTargets returns every persisted TargetRecord in namespace, sorted by
// name for deterministic iteration order.
func ListTargets(ctx context.Context, store provider.Store, namespace string) ([]TargetRecord, error) {
	it, err := store.Scan(ctx, namespace, targetKeyPrefix)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "backup: scan target records")
	}
	defer func() { _ = it.Close() }()

	var recs []TargetRecord
	for it.Next(ctx) {
		rec, derr := decodeTargetRecord(it.Value())
		if derr != nil {
			return nil, derr
		}
		recs = append(recs, rec)
	}
	if iterErr := it.Err(); iterErr != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, iterErr, "backup: scan target records")
	}
	sort.Slice(recs, func(i, k int) bool { return recs[i].Name < recs[k].Name })
	return recs, nil
}

// targetRecordWire is TargetRecord's JSON wire shape.
type targetRecordWire struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	FSRoot       string `json:"fs_root,omitempty"`
	S3EnvPrefix  string `json:"s3_env_prefix,omitempty"`
	RcloneRemote string `json:"rclone_remote,omitempty"`
}

// encodeTargetRecord serializes rec to its Store-persisted JSON form.
func encodeTargetRecord(rec TargetRecord) ([]byte, error) {
	wire := targetRecordWire{
		Name: rec.Name, Kind: string(rec.Kind),
		FSRoot: rec.FSRoot, S3EnvPrefix: rec.S3EnvPrefix, RcloneRemote: rec.RcloneRemote,
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: encode target record")
	}
	return data, nil
}

// decodeTargetRecord deserializes data produced by encodeTargetRecord. It
// returns a cascade.KindIntegrity error, never a panic, for malformed JSON.
func decodeTargetRecord(data []byte) (TargetRecord, error) {
	var wire targetRecordWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return TargetRecord{}, cascade.Wrap(cascade.KindIntegrity, err, "backup: corrupt target record")
	}
	return TargetRecord{
		Name: wire.Name, Kind: TargetKind(wire.Kind),
		FSRoot: wire.FSRoot, S3EnvPrefix: wire.S3EnvPrefix, RcloneRemote: wire.RcloneRemote,
	}, nil
}

// BuildTarget constructs S-41.T3's real driver for rec by kind: fs needs
// neither engine nor getenv; s3 resolves its four env-refs from getenv via
// rec.S3EnvPrefix and requires a non-nil engine (the backup-target egress
// capability, R-21.265); rclone requires a non-nil engine and runs the real
// rclone binary (runner nil -- see targets.NewRcloneTarget). An unknown
// kind refuses with KindInvalidInput rather than silently defaulting to
// any one driver.
func BuildTarget(ctx context.Context, rec TargetRecord, engine *egress.Engine, getenv func(string) string) (Target, error) {
	switch rec.Kind {
	case TargetKindFS:
		return targets.NewFSTarget(rec.FSRoot)
	case TargetKindS3:
		cfg, err := targets.ResolveS3TargetEnvRefs(getenv, rec.S3EnvPrefix)
		if err != nil {
			return nil, err
		}
		return targets.NewS3Target(ctx, cfg, engine)
	case TargetKindRclone:
		return targets.NewRcloneTarget(rec.RcloneRemote, nil, engine)
	default:
		return nil, cascade.Newf(cascade.KindInvalidInput, "backup: unknown target kind %q", rec.Kind)
	}
}
