package plugins

// Purpose (this file): the artifact-verification half of installerAdapter
//   (cascadepa_install_installer.go) -- split out as its own file so the
//   installer stays under the 300-line cap. Fetches an entry's artifact
//   bytes and verifies them, once, with the real
//   pkg/plugin.Ed25519Verifier (checksum + Ed25519 signature) BEFORE
//   anything downstream (ParseManifest, AddPlugin, ProvisionElevated) ever
//   touches them, and caches the verified buffer so a later call for the
//   SAME plugin+version (the elevated retry) never re-fetches -- closing
//   the TOCTOU window round-1 adversarial CR B1 found (a tampered second
//   fetch installed under the first fetch's honest checksum).
// Inputs: a plugin id and the RegistryVersionEntry naming the artifact's
//   checksum and signature.
// Outputs: the verified artifact bytes, or the first typed refusal: empty
//   checksum, empty signature, unconfigured registry URL/public key,
//   fetch failure, or a verification failure from the real verifier.
// Constraints: fail closed on every path (Art.1/Art.3) -- an empty
//   Checksum or Signature is refused BEFORE any network call, never
//   treated as "nothing to check" the way pkg/plugin.Ed25519Verifier's own
//   generic VerifyArtifact treats an empty Signature (that method's
//   contract is intentionally permissive for callers that never carry a
//   signature at all; this ticket's own policy requires both).
// SPORT: internal/plugins:cascadepa-install-wiring (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"crypto/ed25519"
	"strings"

	"github.com/acamarata/cascade/internal/plugins/registryfetch"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// errNoRegistryConfigured is the production registryURL default's typed
// refusal.
var errNoRegistryConfigured = cascade.New(cascade.KindUnavailable,
	"cascade-pa install: no plugin registry is configured -- the [registry].url config reader "+
		"(X/S-50.T2, internal/runtime/config_registry.go) is not this ticket's dependency and is "+
		"not yet landed on this branch; a registry-sourced candidate cannot be installed until a "+
		"later composition root injects the real reader")

// errNoRegistryPubKeyConfigured is the production registryPubKey default's
// typed refusal -- the same disclosed gap as errNoRegistryConfigured,
// naming the verification key rather than the fetch URL.
var errNoRegistryPubKeyConfigured = cascade.New(cascade.KindUnavailable,
	"cascade-pa install: no registry public key is configured; a registry-sourced artifact cannot "+
		"be verified, so it cannot be installed, until a later composition root injects the real "+
		"[registry].public_key reader")

// verifiedArtifactBytes returns the fetched artifact bytes for entry,
// verified once against the real per-artifact verifier (checksum +
// Ed25519 signature), and cached keyed by {pluginID, entry.Version} so a
// later call for the SAME version (the elevated retry) reuses the already-
// verified buffer instead of fetching -- and trusting -- a second time.
func (a *installerAdapter) verifiedArtifactBytes(ctx context.Context, pluginID string, entry plugin.RegistryVersionEntry) ([]byte, error) {
	key := pluginID + "@" + entry.Version
	a.verifyMu.Lock()
	cached, ok := a.verified[key]
	a.verifyMu.Unlock()
	if ok {
		return cached, nil
	}

	if entry.Checksum == "" {
		return nil, cascade.Newf(cascade.KindPolicyDenied,
			"cascade-pa install: registry entry %q v%s has no checksum; refusing to install unverifiable bytes",
			pluginID, entry.Version)
	}
	if entry.Signature == "" {
		return nil, cascade.Newf(cascade.KindPolicyDenied,
			"cascade-pa install: registry entry %q v%s has no signature; refusing to install unverifiable bytes",
			pluginID, entry.Version)
	}
	// round-1 adversarial CR fix item 2 (second half): entry.DownloadURL is
	// passed verbatim to the fetcher with no scheme check
	// (registryfetch.HTTPFetcher.FetchArtifact); a plaintext http:// URL
	// combined with a MITM makes the checksum/signature check moot (an
	// attacker controlling the transport controls what gets checksummed).
	// Refuse before ever fetching, same as the empty-checksum/signature
	// guards above.
	if err := refuseNonHTTPS("download url", entry.DownloadURL, pluginID, entry.Version); err != nil {
		return nil, err
	}

	baseURL, pub, err := a.resolveRegistryConfig()
	if err != nil {
		return nil, err
	}
	// round-2 rework (T0 decision D5): the DownloadURL guard above only
	// ever fires when entry.DownloadURL is set. When it is empty the
	// fetcher falls back to this [registry] base url, whose scheme was
	// never checked -- a plaintext [registry].url still fetched.
	if err := refuseNonHTTPS("registry base url", baseURL, pluginID, entry.Version); err != nil {
		return nil, err
	}

	fetch := a.fetcher
	if fetch == nil {
		fetch = func(baseURL string) plugin.RegistryFetcher { return registryfetch.HTTPFetcher{BaseURL: baseURL} }
	}
	artifact, err := fetch(baseURL).FetchArtifact(ctx, entry)
	if err != nil {
		return nil, err
	}
	if err := (plugin.Ed25519Verifier{PublicKey: pub}).VerifyArtifact(ctx, artifact, entry); err != nil {
		return nil, err
	}

	a.verifyMu.Lock()
	if a.verified == nil {
		a.verified = map[string][]byte{}
	}
	a.verified[key] = artifact
	a.verifyMu.Unlock()
	return artifact, nil
}

// refuseNonHTTPS refuses when url is set but not https -- the shared guard
// behind both the DownloadURL check (round-1 CR fix item 2) and the
// registry base url check (round-2 rework, T0 decision D5). An empty url
// is not this guard's concern: the DownloadURL call site treats "unset" as
// "derive from the registry base" (checked separately, right after), and
// the registry-base call site never sees an empty url in practice --
// resolveRegistryConfig's own unconfigured default already refused one.
func refuseNonHTTPS(field, url, pluginID, version string) error {
	if url == "" || strings.HasPrefix(url, "https://") {
		return nil
	}
	return cascade.Newf(cascade.KindPolicyDenied,
		"cascade-pa install: registry entry %q v%s has a non-https %s %q; refusing a plaintext fetch",
		pluginID, version, field, url)
}

// resolveRegistryConfig resolves the registry base URL and public key
// through their injected resolvers, or their REFUSING defaults when unset.
func (a *installerAdapter) resolveRegistryConfig() (string, ed25519.PublicKey, error) {
	resolveURL := a.registryURL
	if resolveURL == nil {
		resolveURL = func() (string, error) { return "", errNoRegistryConfigured }
	}
	baseURL, err := resolveURL()
	if err != nil {
		return "", nil, err
	}
	resolvePubKey := a.registryPubKey
	if resolvePubKey == nil {
		resolvePubKey = func() (ed25519.PublicKey, error) { return nil, errNoRegistryPubKeyConfigured }
	}
	pub, err := resolvePubKey()
	if err != nil {
		return "", nil, err
	}
	return baseURL, pub, nil
}
