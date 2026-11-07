// Purpose: newEvidenceKeyFn, the one function that computes a provider's
// billing evidence key: the promote writes evidence rows with it and the
// prune re-reads them with it. A key binds the provider's identity (the
// registry CreatedAt instance, the endpoint, the auth ref and a keyed
// BLAKE3 fingerprint of the credential), never its name alone, so a
// provider removed and re-added under the same name, re-pointed at another
// endpoint or re-keyed in the vault no longer matches its old evidence.
// Inputs: the provider registry, the standing-grant credential source, the
// daemon's admitted gateway accounts, the harness agent lanes and their
// shim dependencies, and a per-install fingerprint key file.
// Outputs: topology.EvidenceKey values and the frozen-kind errors below.
// Constraints: a credential value is hashed and dropped; it never appears
// in a key, log or error. A gateway key reads no vault and runs no helper:
// it fingerprints the admitted account's declared shape only.
// SPORT: cmd/cascade billing evidence (NEW, P1-TOP-19c).

package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strings"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/agents"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	"github.com/acamarata/cascade/internal/providers/gateway"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// evidenceKeyBytes is the per-install fingerprint key length.
const evidenceKeyBytes = 32

// evidenceFingerprintBytes is the truncated digest length (32 hex chars).
const evidenceFingerprintBytes = 16

// evidenceNoCredential is the fingerprint of a provider that holds no key.
const evidenceNoCredential = "none"

// providerGetter is the registry read the key needs.
type providerGetter interface {
	GetProvider(ctx context.Context, name string) (providerregistry.ProviderRecord, error)
}

// evidenceKeyDeps are the inputs of newEvidenceKeyFn. Credentials is the
// standing-grant source (the only credential source); Gateways are the
// ADMITTED accounts only; Agents (may be nil) and Shim resolve an
// "agent:<harness>" account; KeyPath is the per-install fingerprint key file.
type evidenceKeyDeps struct {
	Registry    providerGetter
	Credentials providerdispatch.CredentialSource
	Gateways    []gateway.Account
	Agents      agentLanes
	Shim        agents.ShimDeps
	KeyPath     string
}

// newEvidenceKeyFn loads (or creates, 0600 from crypto/rand) the
// fingerprint key and returns the key function. A key file with a mode
// wider than 0600, a short key or an unreadable path is refused.
func newEvidenceKeyFn(d evidenceKeyDeps) (topology.EvidenceKeyFn, error) {
	if d.Registry == nil || d.Credentials == nil || d.KeyPath == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "evidence key: registry, credentials and key path are required")
	}
	key, err := loadEvidenceKey(d.KeyPath)
	if err != nil {
		return nil, err
	}
	accts := append([]gateway.Account(nil), d.Gateways...)
	return func(ctx context.Context, id topology.AccountID) (topology.EvidenceKey, error) {
		switch {
		case !id.Valid():
			return topology.EvidenceKey{}, cascade.New(cascade.KindInvalidInput, "evidence key: account id is required")
		case strings.HasPrefix(string(id), agents.LanePrefix):
			return agentEvidenceKey(d, key, string(id))
		default:
			return registryEvidenceKey(ctx, d, accts, key, id)
		}
	}, nil
}

// registryEvidenceKey builds the key for one registry provider.
func registryEvidenceKey(ctx context.Context, d evidenceKeyDeps, accts []gateway.Account, key []byte, id topology.AccountID) (topology.EvidenceKey, error) {
	rec, err := d.Registry.GetProvider(ctx, string(id))
	if err != nil {
		kind, ok := cascade.KindOf(err)
		if !ok {
			kind = cascade.KindUnavailable
		}
		return topology.EvidenceKey{}, cascade.Wrapf(kind, err, "evidence key: provider %q", string(id))
	}
	k := topology.EvidenceKey{
		Provider:   rec.Name,
		InstanceID: fmt.Sprintf("%s@%d", rec.Name, rec.CreatedAt.UnixMilli()),
		Endpoint:   rec.BaseURL,
		AuthRef:    rec.AuthRef.String(),
	}
	switch {
	case rec.Auth == providerregistry.AuthGateway:
		k.CredentialFingerprint, err = gatewayFingerprint(key, accts, rec)
	case keylessDriver(rec.Driver):
		k.CredentialFingerprint = evidenceNoCredential
	default:
		k.CredentialFingerprint, err = vaultFingerprint(ctx, d.Credentials, key, rec)
	}
	if err != nil {
		return topology.EvidenceKey{}, err
	}
	return k, nil
}

// keylessDriver reports whether a driver carries no credential.
func keylessDriver(k providerregistry.DriverKind) bool {
	return k == providerregistry.DriverOllama || k == providerregistry.DriverLocalLLM
}

// vaultFingerprint resolves the standing-grant credential and hashes it.
func vaultFingerprint(ctx context.Context, src providerdispatch.CredentialSource, key []byte, rec providerregistry.ProviderRecord) (string, error) {
	value, err := src.Resolve(ctx, rec.AuthRef.String())
	if err != nil {
		kind, ok := cascade.KindOf(err)
		if !ok {
			kind = cascade.KindUnavailable
		}
		return "", cascade.Wrapf(kind, err, "evidence key: credential for provider %q", rec.Name)
	}
	if value == "" {
		return evidenceNoCredential, nil
	}
	return keyedFingerprint(key, "vault", value), nil
}

// gatewayFingerprint hashes the admitted account's declared shape. It
// reads no vault and runs no helper.
func gatewayFingerprint(key []byte, accts []gateway.Account, rec providerregistry.ProviderRecord) (string, error) {
	name := strings.TrimPrefix(rec.AuthRef.String(), "gateway:")
	acct, err := gateway.Lookup(accts, name)
	if err != nil {
		return "", cascade.Newf(cascade.KindPermissionDenied,
			"gateway account %s is not admitted; run `cascade config accept` after reviewing [gateways.%s]", name, name)
	}
	if acct.BaseURL != rec.BaseURL {
		return "", cascade.Newf(cascade.KindConflict, "evidence key: provider %q base URL differs from its gateway account", rec.Name)
	}
	parts := []string{"gateway", acct.Name, acct.BaseURL, string(acct.Header), string(acct.Credential.Kind)}
	if acct.Credential.Kind == gateway.CredentialHelper {
		parts = append(parts, acct.Credential.HelperPath, acct.Credential.HelperSHA256)
	} else {
		parts = append(parts, acct.Credential.EnvVar)
	}
	return keyedFingerprint(key, parts...), nil
}

// keyedFingerprint is BLAKE3 over the install key then each part,
// length-prefixed so no two part lists collide, truncated to 32 hex.
func keyedFingerprint(key []byte, parts ...string) string {
	h := blake3.New()
	_, _ = h.Write(key) // hash.Hash.Write never returns an error
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:evidenceFingerprintBytes])
}

// loadEvidenceKey reads the key at path, creating it when absent.
func loadEvidenceKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return createEvidenceKey(path)
	}
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "evidence key: could not stat the key file %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, cascade.Newf(cascade.KindIntegrity, "evidence key: the key file %s is not a regular file", path)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, cascade.Newf(cascade.KindIntegrity, "evidence key: the key file %s has mode %#o, want 0600", path, info.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "evidence key: could not read the key file %s", path)
	}
	defer func() { _ = f.Close() }()
	key, err := io.ReadAll(io.LimitReader(f, evidenceKeyBytes+1))
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "evidence key: could not read the key file %s", path)
	}
	if len(key) != evidenceKeyBytes {
		return nil, cascade.Newf(cascade.KindIntegrity, "evidence key: the key file %s holds %d bytes, want %d", path, len(key), evidenceKeyBytes)
	}
	return key, nil
}

// createEvidenceKey writes a fresh crypto/rand key with mode 0600.
func createEvidenceKey(path string) ([]byte, error) {
	key := make([]byte, evidenceKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence key: could not generate the key")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "evidence key: could not create the key file %s", path)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "evidence key: could not write the key file %s", path)
	}
	if err := f.Close(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "evidence key: could not close the key file %s", path)
	}
	return key, nil
}
