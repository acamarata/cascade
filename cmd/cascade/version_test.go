// Purpose: `cascade version` prints the channel internal/update resolves.
//
// SPORT: cmd/cascade version/CHANGED.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/update"
)

func TestVersionPrintsResolvedChannel(t *testing.T) {
	dir := t.TempDir()
	receipt := filepath.Join(dir, "install-receipt.json")
	sum := strings.Repeat("a", 64)
	body := `{"schema_version":1,"channel":"script","release_tag":"v2.0.0","archive_name":"a","archive_sha256":"` + sum +
		`","install_dir":` + jsonString(dir) + `,"installed_at":"x","files":[{"name":"` + exeFor("cascade") + `","sha256":"` + sum + `"}]}`
	if err := os.WriteFile(receipt, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "bin", "cascade")
	cases := []struct{ name, exe, receipt, want string }{
		{"receipt", plain, receipt, "channel: script"},
		{"caskroom", "/opt/homebrew/Caskroom/cascade/2.0.0/cascade", receipt, "channel: brew"},
		{"none", plain, filepath.Join(dir, "missing.json"), "channel: manual"},
	}
	orig := versionEnv
	t.Cleanup(func() { versionEnv = orig })
	for _, tc := range cases {
		versionEnv = func() update.Env {
			return update.Env{Executable: func() (string, error) { return tc.exe, nil }, ReceiptPath: tc.receipt}
		}
		out, err := execRoot(t, "version")
		if err != nil || !strings.Contains(out, tc.want+"\n") {
			t.Fatalf("%s: version = %q, %v; want %q", tc.name, out, err, tc.want)
		}
	}
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

func exeFor(base string) string {
	if filepath.Separator == '\\' {
		return base + ".exe"
	}
	return base
}
