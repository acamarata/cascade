package build

// Purpose: no test may reach the OPERATOR'S REAL credential store. A test
//   calling secrets.SelectCustody with a Service but no Runner gets the
//   platform backend on a host that has one, which on macOS is the login
//   keychain: the test then creates real keychain items under the real
//   user's account. That happened during H/S-16.T5 (three items created and
//   afterwards deleted), and it is worse than the HOME pollution Art.7.1
//   already forbids, because the keychain is not under HOME and no
//   redirected-HOME lane can catch it.
// Inputs: every _test.go file in the real tree.
// Outputs: a failure naming each call site that could reach a platform
//   backend.
// Constraints: a failing Runner is what makes the platform backend report
//   unavailable, so requiring one is the check. Platform-tagged tests that
//   deliberately exercise real selection are allow-listed BY PATH with a
//   reason, never by silently skipping a directory.
// SPORT: internal.build.KeychainGate/ADDED (T0, R-14.206).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keychainGateAllow lists the tests that legitimately call SelectCustody
// without a failing Runner. Both are build-tagged for a platform this
// machine does not run, and both exist to assert the real selector falls
// back when that platform's backend is absent, which a stubbed Runner
// would defeat.
var keychainGateAllow = map[string]string{
	"internal/secrets/custody_linux_test.go":   "//go:build linux; asserts fallback when secret-service is unavailable",
	"internal/secrets/custody_windows_test.go": "//go:build windows; asserts fallback when no native backend exists",
}

func TestNoTestReachesTheRealKeychain(t *testing.T) {
	root := coverageModuleRoot(t)
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".cover", "node_modules", "seeded-violations":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if _, ok := keychainGateAllow[filepath.ToSlash(rel)]; ok {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "SelectCustody(") {
				continue
			}
			// Look ahead over the composite literal. A Runner that fails is
			// what forces the file vault; without one, a host with a
			// platform backend hands back the real store.
			window := strings.Join(lines[i:min(i+14, len(lines))], "\n")
			if strings.Contains(window, "Runner:") {
				continue
			}
			// No Service means no platform backend is even attempted, so
			// such a call cannot reach the real store.
			if !strings.Contains(window, "Service:") {
				continue
			}
			offenders = append(offenders,
				filepath.ToSlash(rel)+":"+itoa(i+1)+" SelectCustody with a Service and no Runner")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("keychain gate: walking the tree: %v", err)
	}
	if len(offenders) != 0 {
		t.Fatalf("keychain gate: %d test call site(s) could write to the operator's real credential store.\n"+
			"Pass a Runner that returns an error so the platform backend reports unavailable:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// itoa avoids pulling strconv in for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
