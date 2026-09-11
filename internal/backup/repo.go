// Purpose: the target-agnostic backup repository layout (05-PEWS-PLAN-W4-W6
//
//	§Epic S S-41.T1) — the key-naming convention every later Epic S ticket
//	builds on (manifests: S-41.T2, targets: S-41.T3, restore: S-41.T4) —
//	plus the Target abstraction those drivers implement and the
//	content-addressed object accessors (PutObject/HasObject/GetObject)
//	Dedup and Pipeline call.
//
// Inputs: a Target implementation (S-41.T3 supplies fs/s3/rclone; tests use
//
//	an in-memory fake) and, for the config accessors, a RepoConfig.
//
// Outputs: sharded object keys and the repo-level config document.
// Constraints: this file never touches a filesystem or network path
//
//	directly — everything is expressed through Target, so the layout is
//	provable against an in-memory fake with no `net` import anywhere in
//	this package (the default unit lane forbids it).
//
// SPORT: internal.backup.repo/ADDED (P1-E19-W4-S41-T1).

package backup

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Layout key prefixes (05 §Epic S / refs/chatgpt-2026-08-30 §20): every key
// any Target ever sees is one of these three prefixes, or the fixed config
// document below. Everything beyond the repo root is opaque ciphertext —
// only repoConfigKey (the public age recipient) and key names/shapes are
// ever visible to a target operator.
const (
	repoConfigDir    = "config"
	repoManifestsDir = "manifests"
	repoObjectsDir   = "objects"
	repoConfigKey    = repoConfigDir + "/repo.json"
	// objectShardLen is the hex-prefix length objects/ shards on, matching
	// the restic/Borg-style layout the ticket's spec_refs name.
	objectShardLen = 2
)

// Target is the object-store abstraction every backup repository target
// (S-41.T3's fs/s3/rclone drivers) implements over this ticket's layout.
// Get MUST return a *cascade.Error with Kind cascade.KindNotFound when key
// does not exist — HasObject and ReadRepoConfig depend on that contract to
// distinguish "absent" from a real I/O failure.
type Target interface {
	// Put stores r's full content under key, replacing any prior value.
	Put(ctx context.Context, key string, r io.Reader) error
	// Get returns key's stored content, or a KindNotFound *cascade.Error
	// if key is absent.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns every stored key with the given prefix.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes key. Deleting an absent key is not an error.
	Delete(ctx context.Context, key string) error
}

// ObjectKey returns the sharded objects/ key for a content hash, so
// identical hashes always land at the identical key regardless of which
// Target stores them ("only new/changed chunks upload").
func ObjectKey(hash [32]byte) string {
	h := hex.EncodeToString(hash[:])
	return repoObjectsDir + "/" + h[:objectShardLen] + "/" + h
}

// HasObject reports whether an object with the given hash is already
// stored in t, distinguishing "absent" (false, nil) from a real read
// failure (false, err).
func HasObject(ctx context.Context, t Target, hash [32]byte) (bool, error) {
	rc, err := t.Get(ctx, ObjectKey(hash))
	if err == nil {
		_ = rc.Close()
		return true, nil
	}
	if cascade.HasKind(err, cascade.KindNotFound) {
		return false, nil
	}
	return false, cascade.Wrap(cascade.KindUnavailable, err, "backup: probe object")
}

// PutObject stores payload under hash's sharded key, skipping the write
// (and reporting exists=true) when an object with that id is already
// present — the dedup half of "only new/changed chunks upload". payload is
// whatever Dedup's caller supplies (the already compressed+encrypted
// bytes); PutObject itself performs no transform.
func PutObject(ctx context.Context, t Target, hash [32]byte, payload []byte) (exists bool, err error) {
	exists, err = HasObject(ctx, t, hash)
	if err != nil {
		return false, err
	}
	if exists {
		return true, nil
	}
	if err := t.Put(ctx, ObjectKey(hash), bytes.NewReader(payload)); err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "backup: store object")
	}
	return false, nil
}

// GetObject reads back the stored payload for hash, refusing (KindNotFound)
// when it is absent.
func GetObject(ctx context.Context, t Target, hash [32]byte) ([]byte, error) {
	rc, err := t.Get(ctx, ObjectKey(hash))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, err
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "backup: read object")
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "backup: read object body")
	}
	return data, nil
}

// WriteRepoConfig encodes cfg and stores it at the repo's fixed config key.
// cfg's own validation (EncodeRepoConfig) is the only gate here; the config
// document carries the age RECIPIENT only — public material, never a
// private identity (00-VISION principle 10).
func WriteRepoConfig(ctx context.Context, t Target, cfg RepoConfig) error {
	data, err := EncodeRepoConfig(cfg)
	if err != nil {
		return err
	}
	if err := t.Put(ctx, repoConfigKey, bytes.NewReader(data)); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: write repo config")
	}
	return nil
}

// ReadRepoConfig reads and decodes the repo's config document, refusing
// (KindNotFound) when no repo has been initialized at t yet.
func ReadRepoConfig(ctx context.Context, t Target) (RepoConfig, error) {
	rc, err := t.Get(ctx, repoConfigKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return RepoConfig{}, err
		}
		return RepoConfig{}, cascade.Wrap(cascade.KindUnavailable, err, "backup: read repo config")
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return RepoConfig{}, cascade.Wrap(cascade.KindUnavailable, err, "backup: read repo config body")
	}
	return DecodeRepoConfig(data)
}
