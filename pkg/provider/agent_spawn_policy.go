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
	"regexp"

	"github.com/acamarata/cascade/pkg/cascade"
)

// driverEnvAllowlistBase is the R-21.151 normative set every driver child
// inherits regardless of vendor: PATH, HOME, TMPDIR, LANG, the job/socket
// identity, and the H/S-16.T1 broker-proxy variables.
var driverEnvAllowlistBase = []string{
	"PATH", "HOME", "TMPDIR", "LANG",
	"CASCADE_JOB_ID", "CASCADE_SOCKET",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
}

// DriverEnvAllowlist returns the trusted-driver environment allowlist: the
// normative base set plus the driver's own vendorAuthVar name. Everything
// else is stripped by the shim — the child never inherits the daemon
// environ. HOME on this allowlist names the per-profile RUNTIME home
// (<state>/agents/profiles/<profile-id>/home, mode 0700, driver-only),
// never the user's real home.
func DriverEnvAllowlist(vendorAuthVar string) []string {
	out := make([]string, 0, len(driverEnvAllowlistBase)+1)
	out = append(out, driverEnvAllowlistBase...)
	if vendorAuthVar != "" {
		out = append(out, vendorAuthVar)
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

// secretPatterns are the credential shapes PreSpawnScan checks for. Every
// literal here is a PATTERN, not a real credential value.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`ghp_[0-9A-Za-z]{36}`),
	regexp.MustCompile(`xox[baprs]-[0-9A-Za-z-]{10,}`),
}

// PreSpawnScan is the pkg-typed seam the shim runs BEFORE every spawn: it
// walks root restricted to scope's InScopePrefixes plus DeclaredDeps and
// checks each regular file's contents against secretPatterns. A hit
// aborts the spawn with ErrPreSpawnSecretFound; any I/O error scanning the
// tree is ALSO fail-closed (the spawn is aborted) rather than treated as
// an absence of secrets.
func PreSpawnScan(root string, scope WorktreeScope) error {
	prefixes := scope.InScopePrefixes
	if len(scope.DeclaredDeps) > 0 {
		prefixes = append(append([]string{}, prefixes...), scope.DeclaredDeps...)
	}
	if len(prefixes) == 0 {
		prefixes = []string{"."}
	}
	for _, p := range prefixes {
		if err := scanPrefix(filepath.Join(root, p)); err != nil {
			return err
		}
	}
	return nil
}

// scanPrefix walks one prefix rooted at dir, fail-closed on any walk or
// read error.
func scanPrefix(dir string) error {
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
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return cascade.Wrapf(cascade.KindInternal, readErr, "provider: pre-spawn scan could not read %s", path)
		}
		for _, pat := range secretPatterns {
			if pat.Match(data) {
				return ErrPreSpawnSecretFound
			}
		}
		return nil
	})
}
