package update

import (
	"os"
	"path/filepath"
	"testing"
)

func receiptJSON(channel, installDir string) string {
	return `{"schema_version":1,"channel":"` + channel + `","release_tag":"v2.0.0","archive_name":"cascade_2.0.0_linux_amd64.tar.gz",` +
		`"archive_sha256":"` + hexSum([]byte("a")) + `","install_dir":"` + installDir + `","installed_at":"2026-01-01T00:00:00Z",` +
		`"files":[{"name":"cascade","sha256":"` + hexSum([]byte("c")) + `"}]}`
}

// TestResolveChannel is acceptance [3]: stamp, then Caskroom path, then
// receipt, then manual; an invalid stamp is ignored.
func TestResolveChannel(t *testing.T) {
	dir := t.TempDir()
	scriptReceipt := filepath.Join(dir, "script.json")
	must(t, os.WriteFile(scriptReceipt, []byte(receiptJSON("script", dir)), 0o644))
	exe := func(p string) func() (string, error) { return func() (string, error) { return p, nil } }
	plain := filepath.Join(dir, "bin", "cascade")
	cask := "/opt/homebrew/Caskroom/cascade/2.0.0/cascade"
	cases := []struct {
		name, stamp, exe, receipt string
		want                      Channel
	}{
		{"stamp node-managed wins", "node-managed", cask, scriptReceipt, ChannelNodeManaged},
		{"caskroom beats a script receipt", "", cask, scriptReceipt, ChannelBrew},
		{"receipt script", "", plain, scriptReceipt, ChannelScript},
		{"no receipt is manual", "", plain, filepath.Join(dir, "missing.json"), ChannelManual},
		{"invalid stamp is ignored", "bogus", plain, scriptReceipt, ChannelScript},
		{"stamp oci", "oci", plain, scriptReceipt, ChannelOCI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ResolveChannel(Env{Stamp: tc.stamp, Executable: exe(tc.exe), ReceiptPath: tc.receipt, Getenv: func(string) string { return "" }})
			if got != tc.want || reason == "" {
				t.Fatalf("ResolveChannel = %q (%q), want %q with a reason", got, reason, tc.want)
			}
		})
	}
}
