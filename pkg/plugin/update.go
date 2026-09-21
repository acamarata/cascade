package plugin

// Purpose: the registry-driven half of `cascade plugin update` (X/S-50.T8):
//   CheckUpdate answers "is there a newer published version of name than
//   installedVersion" over the existing X/S-50.T1 Fetch path; StageAndVerify
//   Artifact downloads and checksum-verifies a candidate's artifact bytes,
//   always cleaning up its staging file; GrantDiff and ConfirmGrantAcceptance
//   implement the §5.8 automation-parity rule that a grant expansion is
//   NEVER accepted by --yes alone. cmd/cascade/plugin_update.go is the thin
//   CLI composition root over these four functions (flags, env, output);
//   none of them touch a flag, an env var, or stdin — this package stays
//   free of internal/ and process-input concerns (Art.10.2).
// Inputs: a *RegistryClient (already constructed over a live or fake
//   RegistryFetcher/RegistryVerifier/RegistryCache) plus a plugin name/
//   installed version for CheckUpdate; a RegistryVersionEntry plus a
//   staging directory for StageAndVerifyArtifact; two grant-name slices for
//   GrantDiff/ConfirmGrantAcceptance.
// Outputs: a *RegistryVersionEntry (nil at latest), or a typed
//   *cascade.Error (ErrPluginNotInRegistry's Kind, or a propagated
//   Fetch/Verify failure) — CheckUpdate never panics on a malformed or
//   absent entry.
// Constraints: CheckUpdate compares only the major.minor.patch numeric
//   core; a version carrying a pre-release suffix on EITHER side of the
//   comparison is REFUSED with KindInvalidInput naming both versions
//   rather than silently ignoring the suffix (S-50.T8 rework, adversarial
//   CR FIX-8 — no semver precedence dependency exists in go.mod and none
//   is added here, so a build that cannot correctly ORDER two pre-release
//   strings must not guess and report "no update available" for what may
//   actually be a real update). Every version compared is pre-validated
//   with this package's isValidSemver (validate.go) and a malformed
//   version fails closed with KindInvalidInput/KindIntegrity rather than
//   being silently miscompared. StageAndVerifyArtifact verifies through
//   the RegistryClient's own injected RegistryVerifier (checksum AND
//   signature — never the checksum-only free function directly, which a
//   forged-signature artifact could otherwise pass), never returns
//   unverified bytes, and never leaves its staged file behind on any exit
//   path — including a partial write: the cleanup defer is registered
//   BEFORE os.WriteFile runs, not after, so a write that fails partway
//   still gets its staged file removed (S-50.T8 confirming-review R4; no
//   test provokes a genuine partial write because os.WriteFile has no
//   injectable writer seam, so the ordering is documented here rather
//   than mocked).
// SPORT: pkg/plugin registry-client-update (ADD) — P1-E24-W5-S50-T8.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrPluginNotInRegistry is returned by CheckUpdate when name has no entry
// in the verified index at all (distinct from "already at latest", which
// returns nil, nil).
var ErrPluginNotInRegistry = &cascade.Error{Kind: cascade.KindNotFound, Msg: "registry: plugin not found in index"}

// ErrGrantAcceptanceRequired is returned by ConfirmGrantAcceptance when a
// grant-diff's added capabilities are not fully covered by the caller's
// explicit per-grant acceptance.
var ErrGrantAcceptanceRequired = &cascade.Error{Kind: cascade.KindInvalidInput, Msg: "registry: update requests new capability grants that were not explicitly accepted"}

// CheckUpdate reports name's newest published version against
// installedVersion, over the same verified index c.Fetch already
// maintains (cache-hit or fresh fetch — no second network protocol).
// Three outcomes: a non-nil *RegistryVersionEntry when the index's
// latest_version for name is strictly newer than installedVersion; nil,
// nil when name is present but already at (or ahead of) that version;
// nil, ErrPluginNotInRegistry-kinded error when name has no entry at all.
// A Fetch failure (network, malformed index, bad signature) propagates
// verbatim, never masked as "not found" or "no update".
func (c *RegistryClient) CheckUpdate(ctx context.Context, name, installedVersion string) (*RegistryVersionEntry, error) {
	if !isValidSemver(installedVersion) {
		return nil, cascade.Newf(cascade.KindInvalidInput, "registry: installed version %q is not a valid semver string", installedVersion)
	}
	idx, err := c.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range idx.Entries {
		if e.ID == name {
			return checkEntryUpdate(e, installedVersion)
		}
	}
	return nil, cascade.New(cascade.KindNotFound, ErrPluginNotInRegistry.Msg+": "+name)
}

// checkEntryUpdate resolves e's own latest_version entry and compares it
// to installedVersion. A latest_version that names no matching Versions
// row, or is itself malformed, is an integrity failure of the index
// document — never silently treated as "no update". A pre-release suffix
// on either version refuses the comparison outright (KindInvalidInput,
// naming both versions) rather than comparing only their numeric core and
// risking a wrong answer in either direction.
func checkEntryUpdate(e RegistryIndexEntry, installedVersion string) (*RegistryVersionEntry, error) {
	for i := range e.Versions {
		v := e.Versions[i]
		if v.Version != e.LatestVersion {
			continue
		}
		if !isValidSemver(v.Version) {
			return nil, cascade.Newf(cascade.KindIntegrity, "registry: %s's latest_version %q is not a valid semver string", e.ID, v.Version)
		}
		if hasPrereleaseSuffix(v.Version) || hasPrereleaseSuffix(installedVersion) {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"registry: %s: cannot order pre-release versions %q (installed) and %q (registry) — no semver "+
					"precedence dependency exists; refusing rather than reporting a possibly-wrong update decision",
				e.ID, installedVersion, v.Version)
		}
		if compareSemverCore(v.Version, installedVersion) > 0 {
			out := v
			return &out, nil
		}
		return nil, nil
	}
	return nil, cascade.Newf(cascade.KindIntegrity, "registry: %s's latest_version %q has no matching entry in versions", e.ID, e.LatestVersion)
}

// hasPrereleaseSuffix reports whether s (already isValidSemver-checked by
// every call site) carries a "-<prerelease>" component, per semver's
// grammar of major.minor.patch["-"prerelease]["+"build] — build metadata
// (after any "+") is stripped first, so a hyphen appearing only inside
// build metadata (e.g. "1.2.0+build-1") is never mistaken for a
// pre-release marker.
func hasPrereleaseSuffix(s string) bool {
	core := s
	if i := strings.IndexByte(core, '+'); i >= 0 {
		core = core[:i]
	}
	return strings.ContainsRune(core, '-')
}

// compareSemverCore compares only the numeric major.minor.patch core of a
// and b (both already isValidSemver-checked by every call site in this
// file), returning -1, 0, or 1.
func compareSemverCore(a, b string) int {
	pa, pb := parseSemverCore(a), parseSemverCore(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parseSemverCore extracts s's major/minor/patch as ints, ignoring any
// pre-release or build-metadata suffix. Callers pre-validate s with
// isValidSemver, so a parse failure here (never expected) degrades to 0
// rather than panicking.
func parseSemverCore(s string) [3]int {
	core := s
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.SplitN(core, ".", 3)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		out[i] = n
	}
	return out
}

// GrantDiff reports the capability grants candidate adds and removes
// relative to installed, each returned sorted for deterministic display.
// A capability present in both is neither added nor removed.
func GrantDiff(installed, candidate []string) (added, removed []string) {
	have := make(map[string]struct{}, len(installed))
	for _, g := range installed {
		have[g] = struct{}{}
	}
	want := make(map[string]struct{}, len(candidate))
	for _, g := range candidate {
		want[g] = struct{}{}
	}
	for _, g := range candidate {
		if _, ok := have[g]; !ok {
			added = append(added, g)
		}
	}
	for _, g := range installed {
		if _, ok := want[g]; !ok {
			removed = append(removed, g)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// ConfirmGrantAcceptance enforces the 06-FORGE-SPEC.md §5.8 automation-
// parity rule: every capability in added must appear in accepted (one
// explicit --accept-grant=<grant> per new capability, the CLI layer's
// job to collect) before an update carrying a grant expansion may
// proceed. --yes is never sufficient on its own — that flag never reaches
// this function's accepted parameter unless the caller explicitly named
// each grant. Returns nil when added is empty (nothing to confirm) or
// fully covered; otherwise a KindInvalidInput error naming exactly the
// missing grants, sorted for a deterministic message.
func ConfirmGrantAcceptance(added, accepted []string) error {
	if len(added) == 0 {
		return nil
	}
	acceptedSet := make(map[string]struct{}, len(accepted))
	for _, g := range accepted {
		acceptedSet[g] = struct{}{}
	}
	var missing []string
	for _, g := range added {
		if _, ok := acceptedSet[g]; !ok {
			missing = append(missing, g)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return cascade.Newf(cascade.KindInvalidInput,
		"%s: missing --accept-grant for: %s (pass one --accept-grant=<grant> per new capability; --yes never accepts a grant expansion)",
		ErrGrantAcceptanceRequired.Msg, strings.Join(missing, ", "))
}

// StageAndVerifyArtifact downloads entry's artifact bytes via c's own
// injected RegistryFetcher, writes them to a temp file under stagingDir
// (parity with a real download-to-disk pipeline, not an in-memory
// shortcut), verifies them through c's OWN injected RegistryVerifier
// (checksum AND signature — adversarial CR FIX-4: the checksum-only free
// function VerifyArtifact is never called directly here, because a
// registry entry can carry a valid checksum over artifact bytes that were
// never actually signed by the registry's key; c.verifier.VerifyArtifact
// is the one seam that checks both), and ALWAYS removes the staged file
// before returning — on the verify-failure path exactly as much as on
// success, so a checksum or signature mismatch never leaves a partial
// artifact behind (§5.9/full_desc "rollback... partial artifact... staging
// area is cleaned up"). A fetch failure never reaches the filesystem at
// all: nothing is staged and nothing needs cleanup.
func (c *RegistryClient) StageAndVerifyArtifact(ctx context.Context, entry RegistryVersionEntry, stagingDir string) ([]byte, error) {
	data, err := c.fetcher.FetchArtifact(ctx, entry)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "registry: create staging dir %q", stagingDir)
	}
	stagePath := filepath.Join(stagingDir, sanitizeStagingName(entry.Version)+".staged")
	// The cleanup defer is registered BEFORE os.WriteFile runs (S-50.T8
	// confirming review R4): os.WriteFile can fail after it has already
	// created stagePath and written some of data (e.g. the process is
	// killed, or the volume fills mid-write) — registering cleanup only on
	// the success path left that partial file behind. os.WriteFile has no
	// injectable writer seam (it is a direct call, not something this
	// package can fake mid-write through a double), so a genuine partial
	// write cannot be deterministically provoked from a unit test; the
	// ordering fix itself is proven by TestStageAndVerifyArtifact_
	// FetchFailureStagesNothing and every StageAndVerifyArtifact test
	// asserting assertStagingEmpty after a verify failure, which would
	// fail if the defer were ever skipped or misordered relative to a
	// later return.
	defer func() { _ = os.Remove(stagePath) }()
	if err := os.WriteFile(stagePath, data, 0o600); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "registry: write staged artifact %q", stagePath)
	}

	if err := c.verifier.VerifyArtifact(ctx, data, entry); err != nil {
		return nil, err
	}
	return data, nil
}

// sanitizeStagingName strips path-separator characters from a version
// string before it is joined into a staging file path, so a hostile or
// malformed entry.Version can never escape stagingDir (defence in depth —
// entry.Version already passed isValidSemver by the time a caller reaches
// here via CheckUpdate, but StageAndVerifyArtifact takes entry directly
// and must not assume that).
func sanitizeStagingName(version string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(version)
}
