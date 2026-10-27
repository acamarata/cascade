// Package plugins (trust_grant.go): Purpose: the one place a process-tier
// install record becomes trusted.
// Inputs: the handler context of a plugin.add or plugin.update dispatch (the
// elevation middleware's witness and the raw params InstallMiddleware bound),
// the manifest being installed and the checksum the handler was given.
// Outputs: TrustTrusted and the executable's sha256 digest, or a
// KindPolicyDenied refusal naming the condition that failed.
// Constraints: trusted only when the witness names this exact method and the
// hash of the exact raw params; those params decode to this manifest, id and
// checksum; the checksum is non-empty and is the sha256 of the manifest bytes;
// and the first-party executable resolves beside the cascade binary. Nothing
// else (a store write, the cascade-pa install witness, an unelevated call)
// is a trust source. The grant set the witness binds is the manifest's
// Requires; callers record it as TrustGrants.
// SPORT: internal/plugins process trust grant (P1-PLG-10).
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// boundInstallParams is the shape both plugin.add and plugin.update send.
type boundInstallParams struct {
	ID            string `json:"id"`
	Checksum      string `json:"checksum"`
	ManifestBytes []byte `json:"manifest_bytes"`
}

// processTrust is what a grant records on a process-tier record: the tier,
// the executable digest and the bound grant set (TrustGrants).
type processTrust struct {
	Tier   string
	Digest string
	Bound  []string
}

// denyTrust is the refusal every failed condition returns.
func denyTrust(id, method, condition string) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"plugin: %s: process-tier plugin refused (trust_tier not granted at %s): %s; a process plugin is "+
			"trusted only through an elevated `cascade plugin add` or `cascade plugin update`", id, method, condition)
}

// grantProcessTrust returns TrustTrusted and the executable digest when every
// condition in this file's header holds for method ("plugin.add" or
// "plugin.update"), m and checksum; otherwise a KindPolicyDenied refusal.
func grantProcessTrust(ctx context.Context, method string, m plugin.Manifest, checksum string) (tier, digest string, err error) {
	if method != "plugin.add" && method != UpdateRPCMethod {
		return "", "", denyTrust(m.ID, method, "not a plugin install verb")
	}
	raw, err := witnessedInstallParams(ctx, method, m.ID)
	if err != nil {
		return "", "", err
	}
	if err := paramsNameManifest(raw, method, m, checksum); err != nil {
		return "", "", err
	}
	path, err := ResolveExecutable(m.ID)
	if err != nil {
		return "", "", cascade.Wrapf(cascade.KindPolicyDenied, err, "plugin: %s: process-tier plugin refused (trust_tier not granted at %s)", m.ID, method)
	}
	digest, err = ExecutableDigest(path)
	if err != nil {
		return "", "", cascade.Wrapf(cascade.KindPolicyDenied, err, "plugin: %s: process-tier plugin refused (trust_tier not granted at %s)", m.ID, method)
	}
	return TrustTrusted, digest, nil
}

// witnessedInstallParams returns the raw params InstallMiddleware bound when
// ctx carries a witness for method whose params hash is the hash of exactly
// those bytes.
func witnessedInstallParams(ctx context.Context, method, id string) (json.RawMessage, error) {
	w, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok {
		return nil, denyTrust(id, method, "no verified elevation witness")
	}
	if w.Method() != method {
		return nil, denyTrust(id, method, "the elevation witness covers "+w.Method()+", not "+method)
	}
	raw, ok := installParamsFrom(ctx)
	if !ok {
		return nil, denyTrust(id, method, "no install params bound to the call")
	}
	if w.ParamsHash() != rpc.ParamsHash(raw) {
		return nil, denyTrust(id, method, "the elevation witness does not cover these params")
	}
	return raw, nil
}

// paramsNameManifest checks that the witnessed params decode to m's id, the
// handler's checksum and bytes that parse to m, and that the checksum is the
// sha256 of those bytes.
func paramsNameManifest(raw json.RawMessage, method string, m plugin.Manifest, checksum string) error {
	var p boundInstallParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return denyTrust(m.ID, method, "the witnessed params do not decode")
	}
	if p.ID != m.ID {
		return denyTrust(m.ID, method, "the witnessed params name another plugin id")
	}
	if p.Checksum != checksum {
		return denyTrust(m.ID, method, "the witnessed params carry another checksum")
	}
	witnessed, err := plugin.ParseManifest(bytes.NewReader(p.ManifestBytes))
	if err != nil || !reflect.DeepEqual(witnessed, m) {
		return denyTrust(m.ID, method, "the witnessed params name another manifest")
	}
	if checksum == "" {
		return denyTrust(m.ID, method, "no checksum pins the manifest")
	}
	if err := plugin.VerifyArtifact(plugin.RegistryVersionEntry{Checksum: checksum}, p.ManifestBytes); err != nil {
		return denyTrust(m.ID, method, "the checksum is not the sha256 of the manifest bytes")
	}
	return nil
}
