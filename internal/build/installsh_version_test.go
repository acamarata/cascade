package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallScriptRefusesTrustedCommentVersionMismatch uses a real minisign
// signature and the existing offline curl shim; no archive fetch on refusal.
func TestInstallScriptRefusesTrustedCommentVersionMismatch(t *testing.T) {
	for _, tc := range []struct {
		name, archive, comment string
		rc                     int
	}{
		{"older signed version", "2.0.1", "cascade 2.0.1 checksums", 1},
		{"older archive", "2.0.1", "cascade 2.0.2 checksums", 1},
		{"default comment", "2.0.2", "", 1},
		{"matching", "2.0.2", "cascade 2.0.2 checksums", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ishNew(t)
			f.sign()
			archive := "cascade_" + tc.archive + "_linux_amd64.tar.gz"
			sum := f.archive(f.dist, archive, ishExe("cascade"))
			p := filepath.Join(f.dist, "cascade_checksums.txt")
			ishMust(t, os.WriteFile(p, []byte(sum+"  "+archive+"\n"), 0o600))
			args := []string{"-S", "-s", f.key, "-m", p, "-x", p + ".minisig"}
			if tc.comment != "" {
				args = append(args, "-t", tc.comment)
			}
			f.run("minisign", args...)
			f.env = append(f.env, "CASCADE_INSTALL_DIST=", "SHIM_SERVE="+f.dist, "SHIM_LATEST=v2.0.2")
			f.expect(tc.rc)
			if tc.rc == 0 {
				if receipt := f.installed("cascade"); !strings.Contains(receipt, `"release_tag":"v2.0.2"`) {
					t.Fatal("receipt version mismatch")
				}
			} else {
				f.assertNothing(tc.name)
				if strings.Contains(ishRead(t, filepath.Join(f.root, "curl.log")), ".tar.gz") {
					t.Fatal("downloaded archive before version refusal")
				}
			}
		})
	}
}

// TestInstallScriptLocalLaneBindsRequestedTag: an explicit tag binds the
// signed version on the local lane too; a tagless local install still works.
func TestInstallScriptLocalLaneBindsRequestedTag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		rc   int
	}{
		{"newer tag over older signed set", []string{"v9.9.9"}, 1},
		{"matching tag", []string{"v1.0.0"}, 0},
		{"tagless", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ishNew(t)
			f.sign()
			archive := "cascade_1.0.0_linux_amd64.tar.gz"
			f.sums(f.dist, "cascade_1.0.0_checksums.txt", f.archive(f.dist, archive, ishExe("cascade"))+"  "+archive)
			f.expect(tc.rc, tc.args...)
			if tc.rc != 0 {
				f.assertNothing(tc.name)
				return
			}
			if receipt := f.installed("cascade"); !strings.Contains(receipt, `"release_tag":"v1.0.0"`) {
				t.Fatalf("receipt version mismatch: %s", receipt)
			}
		})
	}
}
