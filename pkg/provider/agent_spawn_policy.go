package provider

// Purpose: the R-21.151/R-21.177 normative process-boundary constants the
//
//	AD/S-62.T3 host shim implements, plus the pkg-typed PreSpawnScan seam
//	it runs before every spawn. Nothing in this file executes a spawn.
//
// Inputs: PreSpawnScan takes a worktree root and its declared
//
//	WorktreeScope.
//
// Outputs: PreSpawnScan returns ErrPreSpawnSecretFound on a hit, or a
//
//	typed error on any scan failure — the scan is fail-closed, never
//	silently skipped.
//
// Constraints: DriverEnvAllowlist and ToolEnvAllowlist differ by exactly
//
//	one entry (the per-driver vendorAuthVar). NORMATIVE LIMIT: this
//	package's scan covers driver STDIO and the leased worktree only — it
//	does NOT cover the vendor harness's own TLS network egress, which the
//	harness opens directly. No document or help text may claim driver
//	egress is fully firewalled or fully inspected (binding on the
//	AA/S-55.T2 docs pass). OS-enforced network namespaces are an explicit
//	Art.6 deferral, DEF-P2-driver-netns.
//
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1).

import (
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// driverEnvAllowlistBase is the R-21.151 normative set every driver child
// inherits regardless of vendor: PATH, HOME, TMPDIR, LANG, the job/socket
// identity, the principal token and lease marker, and the six per-spawn
// proxy variables in both cases. ALL_PROXY and all_proxy are deliberately
// absent: the one proxy is the only route out.
var driverEnvAllowlistBase = []string{
	"PATH", "HOME", "TMPDIR", "LANG",
	"CASCADE_JOB_ID", "CASCADE_SOCKET",
	"CASCADE_PRINCIPAL_TOKEN", "CASCADE_LEASE_ID",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
	"https_proxy", "http_proxy", "no_proxy",
}

// DriverEnvAllowlist returns the trusted-driver environment allowlist: the
// normative base set plus each non-empty authVar name (the harness
// config-directory variable and, for a key credential class, its key
// variable). Everything else is stripped by the shim: the child never
// inherits the daemon environ. HOME on this allowlist names the
// per-profile RUNTIME home (<state>/agents/profiles/<profile-id>/home,
// mode 0700, driver-only), never the user's real home.
func DriverEnvAllowlist(authVars ...string) []string {
	out := make([]string, 0, len(driverEnvAllowlistBase)+len(authVars))
	out = append(out, driverEnvAllowlistBase...)
	for _, v := range authVars {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ToolEnvAllowlist returns the untrusted-tool environment allowlist: the
// same normative base set, MINUS the vendorAuthVar — tool subprocesses the
// harness spawns are untrusted and never receive the vendor auth variable.
func ToolEnvAllowlist() []string {
	out := make([]string, len(driverEnvAllowlistBase))
	copy(out, driverEnvAllowlistBase)
	return out
}

// PreSpawnScan is the pkg-typed seam the shim runs BEFORE every spawn: it
// walks root restricted to scope's InScopePrefixes plus DeclaredDeps and
// checks each regular file's contents with match, the caller's content
// matcher (the process's one secret detector; this package holds no pattern
// list of its own). A hit aborts the spawn with ErrPreSpawnSecretFound; any
// I/O error scanning the tree is ALSO fail-closed (the spawn is aborted)
// rather than treated as an absence of secrets, and so is a nil match.
func PreSpawnScan(root string, scope WorktreeScope, match func(content []byte) (class string, hit bool)) error {
	if match == nil {
		return cascade.New(cascade.KindInvalidInput, "provider: pre-spawn scan needs a content matcher")
	}
	prefixes := scope.InScopePrefixes
	if len(scope.DeclaredDeps) > 0 {
		prefixes = append(append([]string{}, prefixes...), scope.DeclaredDeps...)
	}
	if len(prefixes) == 0 {
		prefixes = []string{"."}
	}
	for _, p := range prefixes {
		if err := scanPrefix(filepath.Join(root, p), match); err != nil {
			return err
		}
	}
	return nil
}

// readableAsFile reports whether PreSpawnScan should read path: a regular
// file, or a symlink whose target is one. A FIFO, socket or device (or a
// link to one) holds no stored secret and would block or never end when read.
// A link that cannot be resolved, or that names a directory, is read anyway,
// so its error fails closed.
func readableAsFile(path string, info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Stat(path)
		return err != nil || target.IsDir() || target.Mode().IsRegular()
	}
	return info.Mode().IsRegular()
}

// scanPrefix walks one prefix rooted at dir, fail-closed on any walk or
// read error.
func scanPrefix(dir string, match func([]byte) (string, bool)) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return cascade.Wrapf(cascade.KindInternal, err, "provider: pre-spawn scan failed at %s", path)
		}
		if info.IsDir() {
			return nil
		}
		if !readableAsFile(path, info) {
			return nil // holds no stored secret, and reading a FIFO would block the spawn forever
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return cascade.Wrapf(cascade.KindInternal, readErr, "provider: pre-spawn scan could not read %s", path)
		}
		if _, hit := match(data); hit {
			return ErrPreSpawnSecretFound
		}
		return nil
	})
}
