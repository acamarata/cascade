package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gateReport writes a release-gate report stand-in.
func gateReport(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gate-report.txt")
	put(t, filepath.Dir(p), "gate-report.txt", "gate: pass\n")
	return p
}

func buildArgs(dir, pub, gate, out string) []string {
	return []string{"build", "--rc-tag", e2eRC, "--commit", e2eCommit, "--dir", dir, "--pubkey", pub, "--gate-report", gate, "--out", out}
}

// TestPromotionToolEndToEnd: build then verify a byte copy (0), a copy with
// one flipped byte (1, naming the file), and verify-signed on a genuine (0)
// and a substituted set (1).
func TestPromotionToolEndToEnd(t *testing.T) {
	dir, pub := signedSnapshot(t, newE2EKey(5))
	out := filepath.Join(t.TempDir(), "record.json")
	if code, stdout, errOut := runTool(buildArgs(dir, pub, gateReport(t), out)...); code != 0 || !strings.Contains(stdout, "module tag twin of "+e2eFinal+": v0.3.0") {
		t.Fatalf("build exit %d: %q %q", code, stdout, errOut)
	}
	var rec map[string]any
	if b, err := os.ReadFile(out); err != nil || json.Unmarshal(b, &rec) != nil || rec["promoted_at"] != "2026-10-12T10:00:00Z" {
		t.Fatalf("record not written as JSON with the injected time: %v %v", err, rec)
	}
	public := publicCopy(t, dir)
	if code, stdout, errOut := runTool("verify", "--record", out, "--staging", dir, "--public", public); code != 0 || !strings.Contains(stdout, "14 files") {
		t.Fatalf("verify of a byte copy: exit %d %q %q", code, stdout, errOut)
	}
	archive := "cascade_" + e2eVersion + "_linux_arm64.tar.gz"
	b, _ := os.ReadFile(filepath.Join(public, archive))
	b[100] ^= 1
	put(t, public, archive, string(b))
	if code, _, errOut := runTool("verify", "--record", out, "--staging", dir, "--public", public); code != 1 || !strings.Contains(errOut, "public "+archive) {
		t.Fatalf("flipped byte: exit %d, stderr %q (want 1 naming %s)", code, errOut, archive)
	}
	if code, stdout, errOut := runTool("verify-signed", "--dir", dir, "--pubkey", pub, "--final-tag", e2eFinal); code != 0 || !strings.Contains(stdout, "14 files") {
		t.Fatalf("verify-signed genuine: exit %d %q %q", code, stdout, errOut)
	}
	// Substituted set: an archive replaced and its checksums line rewritten,
	// the old signature left in place.
	old, _ := os.ReadFile(filepath.Join(dir, archive))
	put(t, dir, archive, "evil")
	sums, _ := os.ReadFile(filepath.Join(dir, "cascade_checksums.txt"))
	put(t, dir, "cascade_checksums.txt", strings.Replace(string(sums), sum(old), sum([]byte("evil")), 1))
	if code, _, errOut := runTool("verify-signed", "--dir", dir, "--pubkey", pub, "--final-tag", e2eFinal); code != 1 || !strings.Contains(errOut, "cascade_checksums.txt.minisig does not verify") {
		t.Fatalf("substituted set: exit %d, stderr %q", code, errOut)
	}
}

func TestBuildWritesNothingOnRefusal(t *testing.T) {
	dir, pub := signedSnapshot(t, newE2EKey(5))
	_, otherPub := signedSnapshot(t, newE2EKey(6))
	gate := gateReport(t)
	cases := []struct {
		name string
		args []string
		code int
		frag string
	}{
		{"wrong key", buildArgs(dir, otherPub, gate, ""), 1, "does not verify"},
		{"bad tag", append(buildArgs(dir, pub, gate, ""), "--rc-tag", "v2.3.0"), 2, "rc_tag"},
		{"missing gate report", buildArgs(dir, pub, filepath.Join(dir, "absent"), ""), 2, "gate report"},
		{"missing flag", []string{"build", "--dir", dir}, 2, "required flag"},
		{"missing pubkey file", buildArgs(dir, filepath.Join(dir, "absent.pub"), gate, ""), 2, "cannot read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "record.json")
			args := c.args
			for i, a := range args {
				if a == "--out" {
					args[i+1] = out
				}
			}
			code, _, errOut := runTool(args...)
			if code != c.code || !strings.Contains(errOut, c.frag) {
				t.Fatalf("exit %d stderr %q, want %d with %q", code, errOut, c.code, c.frag)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("a refused build left %s (stat err %v)", out, err)
			}
		})
	}
}

func TestImagesVerb(t *testing.T) {
	dir, pub := signedSnapshot(t, newE2EKey(5))
	base := []string{"images", "--dir", dir, "--pubkey", pub, "--final-tag", e2eFinal}
	cases := []struct {
		extra      []string
		code       int
		stdout, se string
	}{
		{nil, 0, indexSHA + "  cascade\n", ""},
		{[]string{"--name", "cascade"}, 0, indexSHA + "\n", ""},
		{[]string{"--require", indexSHA}, 0, "cascade\n", ""},
		{[]string{"--require", "sha256:" + strings.Repeat("e", 64)}, 1, "", "not in the signed cascade_images.txt"},
		{[]string{"--name", "cascade-linux-riscv64"}, 1, "", "no signed image"},
		{[]string{"--name", "cascade", "--require", indexSHA}, 2, "", "exclusive"},
	}
	for _, c := range cases {
		code, stdout, errOut := runTool(append(append([]string{}, base...), c.extra...)...)
		if code != c.code || !strings.Contains(stdout, c.stdout) || !strings.Contains(errOut, c.se) {
			t.Errorf("images %v: exit %d stdout %q stderr %q", c.extra, code, stdout, errOut)
		}
	}
	put(t, dir, "cascade_images.txt", strings.Replace(indexSHA+"  cascade\n", "c", "d", 1))
	if code, _, errOut := runTool(base...); code != 1 || !strings.Contains(errOut, "cascade_images.txt") {
		t.Fatalf("an edited cascade_images.txt: exit %d %q", code, errOut)
	}
}

func TestCheckKeyVerb(t *testing.T) {
	root := filepath.FromSlash("../../..")
	docs, embedded := filepath.Join(root, "docs", "keys", "minisign.pub"), filepath.Join(root, "internal", "minisign", "release.pub")
	if code, _, errOut := runTool("check-key", "--pub", docs, "--embedded", embedded); code != 0 {
		t.Fatalf("committed key: exit %d %q", code, errOut)
	}
	b, err := os.ReadFile(docs)
	if err != nil {
		t.Fatal(err)
	}
	b[len("untrusted comment: ")] ^= 1
	seeded := filepath.Join(t.TempDir(), "minisign.pub")
	put(t, filepath.Dir(seeded), "minisign.pub", string(b))
	if code, _, errOut := runTool("check-key", "--pub", seeded, "--embedded", embedded); code != 1 || !strings.Contains(errOut, "byte-identical") {
		t.Fatalf("one-byte difference: exit %d %q", code, errOut)
	}
	if code, _, _ := runTool("check-key", "--pub", filepath.Join(t.TempDir(), "absent"), "--embedded", embedded); code != 2 {
		t.Fatalf("missing key file: exit %d, want 2", code)
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"publish"}, {"verify-signed", "--dir", "d", "--pubkey", "p", "--final-tag", "v2.0.0", "stray"}, {"verify", "--bogus"}} {
		if code, _, errOut := runTool(args...); code != 2 || errOut == "" {
			t.Errorf("args %q: exit %d stderr %q, want 2", args, code, errOut)
		}
	}
}
