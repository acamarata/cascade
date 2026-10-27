// widget_release_test.go checks apps/widget-macos/ExportOptions.plist (seeded
// violations in testdata/seeded-violations/widget-release) and the two release
// scripts in .github/scripts through PATH shims (shims/shim.sh stands in for
// xcodegen, xcodebuild, PlistBuddy, codesign, ditto, hdiutil and xcrun).
package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	wrSubmissionID = "0f0e0d0c-0b0a-4908-8706-050403020100"
	wrSeeded       = "internal/build/testdata/seeded-violations/widget-release"
	wrIdentity     = "CASCADE_APPLE_SIGNING_IDENTITY=Developer ID Application: Example (FAKETEAM00)"
	wrBuild, wrSig = "build-widget-release.sh", "sign-notarize-widget.sh"
)

// wrPlistEntry matches one <key> of a flat plist dict and its value element.
var wrPlistEntry = regexp.MustCompile(`<key>([^<]+)</key>\s*<(\w+)\s*(?:/>|>([^<]*)</\w+>)`)

// wrPlistProblems lists every way an ExportOptions plist breaks the contract.
func wrPlistProblems(t *testing.T, path string) []string {
	got, bad := map[string]string{}, []string{}
	for _, m := range wrPlistEntry.FindAllStringSubmatch(ishRead(t, path), -1) {
		got[m[1]] = m[2] + ":" + m[3]
	}
	for k, v := range map[string]string{"method": "string:developer-id", "signingStyle": "string:manual",
		"schemeName": "string:CascadeMenuBar", "stripSwiftSymbols": "true:", "signingCertificate": "", "teamID": ""} {
		if got[k] != v { // the last two belong only in the rendered copy
			bad = append(bad, k+"="+got[k])
		}
	}
	return bad
}

func TestExportOptionsIsDeveloperID(t *testing.T) {
	root := filecapModuleRoot(t)
	if bad := wrPlistProblems(t, filepath.Join(root, "apps", "widget-macos", "ExportOptions.plist")); len(bad) > 0 {
		t.Fatalf("ExportOptions.plist: %v", bad)
	}
	for file, want := range map[string]string{"ExportOptions-development.plist": "method=", "ExportOptions-teamid.plist": "teamID"} {
		if bad := strings.Join(wrPlistProblems(t, filepath.Join(root, filepath.FromSlash(wrSeeded), file)), ";"); !strings.Contains(bad, want) {
			t.Errorf("seeded %s: problems %q do not name %q", file, bad, want)
		}
	}
}

type wrFx struct {
	t                     *testing.T
	root, dir, out, shLog string
	env                   []string
}

// wrNew installs the shims and an $OUT holding the .app the build leaves;
// envs are added to an environment with every inherited CASCADE_* removed.
func wrNew(t *testing.T, envs ...string) *wrFx {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the widget release scripts are bash for the macOS build host")
	}
	d, err := filepath.EvalSymlinks(t.TempDir())
	ishMust(t, err)
	f := &wrFx{t: t, root: filecapModuleRoot(t), dir: d, out: filepath.Join(d, "out"), shLog: filepath.Join(d, "shim.log")}
	bin := filepath.Join(d, "bin")
	ishMust(t, os.MkdirAll(filepath.Join(f.out, "CascadeMenuBar.app", "Contents", "PlugIns", "CascadeWidgetExtension.appex"), 0o755))
	ishMust(t, os.MkdirAll(bin, 0o755))
	shim := ishRead(t, filepath.Join(f.root, filepath.FromSlash(wrSeeded), "shims", "shim.sh"))
	for _, n := range []string{"xcodegen", "xcodebuild", "PlistBuddy", "codesign", "ditto", "hdiutil", "xcrun"} {
		ishMust(t, os.WriteFile(filepath.Join(bin, n), []byte(shim), 0o755))
	}
	f.env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CASCADE_") || strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "SHIM_")
	})
	f.env = append(f.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SHIM_LOG="+f.shLog,
		"SHIM_DIR="+d, "CASCADE_WIDGET_OUT="+f.out)
	f.env = append(f.env, envs...)
	return f
}

// cmd builds one script call; an envs entry "!KEY" unsets KEY.
func (f *wrFx) cmd(script string, envs []string, args ...string) *exec.Cmd {
	c := exec.Command("bash", append([]string{filepath.Join(f.root, ".github", "scripts", script)}, args...)...)
	all := append(append([]string{}, f.env...), envs...)
	for _, kv := range all {
		if !strings.HasPrefix(kv, "!") && !slices.Contains(all, "!"+strings.SplitN(kv, "=", 2)[0]) {
			c.Env = append(c.Env, kv)
		}
	}
	return c
}

// do runs one script call, fails unless it exits as wantOK says, returns output.
func (f *wrFx) do(script string, wantOK bool, envs []string, args ...string) string {
	f.t.Helper()
	b, err := f.cmd(script, envs, args...).CombinedOutput()
	if (err == nil) != wantOK {
		f.t.Fatalf("%s %v %v: want ok=%v, err=%v\n%s\nshims:\n%s", script, args, envs, wantOK, err, b, f.log())
	}
	return string(b)
}

// log is the shim call log; a missing file means no tool ran.
func (f *wrFx) log() string {
	b, _ := os.ReadFile(f.shLog)
	return string(b)
}

// calls counts shim calls of tool whose arguments contain sub.
func (f *wrFx) calls(tool, sub string) int {
	return len(regexp.MustCompile(`(?m)^`+regexp.QuoteMeta(tool)+` .*`+regexp.QuoteMeta(sub)).FindAllString(f.log(), -1))
}

// file returns the content of $OUT/name, or "" when it does not exist.
func (f *wrFx) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.out, filepath.FromSlash(name)))
	return string(b)
}

func TestBuildWidgetScriptAdHocSkipsExport(t *testing.T) {
	f := wrNew(t)
	f.do(wrBuild, true, []string{"CASCADE_APPLE_SIGNING_IDENTITY=-"})
	if f.calls("xcodebuild", "archive") != 1 || f.calls("xcodebuild", "-exportArchive") != 0 || f.calls("PlistBuddy", "") != 0 ||
		f.file("CascadeMenuBar.app/Contents/MacOS/CascadeMenuBar") == "" {
		t.Fatalf("ad-hoc must archive once, never export and leave the .app:\n%s", f.log())
	}
	f = wrNew(t, wrIdentity)
	for name, body := range map[string]string{"notarization.txt": "accepted id=old\n", "CascadeMenuBar.zip": "old",
		"checksums.txt": "00  CascadeMenuBar.zip\n11  other.tar.gz\n", "notary-journal.log": "t submitted id=" + wrSubmissionID + "\n"} {
		ishMust(t, os.WriteFile(filepath.Join(f.out, name), []byte(body), 0o600))
	}
	f.do(wrBuild, true, []string{"CASCADE_APPLE_TEAM_ID=FAKETEAM00"})
	rendered := filepath.Join(f.out, "ExportOptions.rendered.plist")
	if f.calls("PlistBuddy", "Add :signingCertificate string \"Developer ID Application: Example (FAKETEAM00)\" "+rendered) != 1 ||
		f.calls("PlistBuddy", "Add :teamID string \"FAKETEAM00\" "+rendered) != 1 ||
		f.calls("xcodebuild", "-exportArchive -quiet -archivePath") != 1 || f.calls("xcodebuild", "-exportOptionsPlist "+rendered) != 1 {
		t.Fatalf("identity build must render both keys and export with the rendered plist:\n%s", f.log())
	}
	if f.file("notarization.txt") != "" || f.file("CascadeMenuBar.zip") != "" || f.file("checksums.txt") != "11  other.tar.gz\n" ||
		!strings.HasSuffix(f.file("notary-journal.log"), " begin build\n") {
		t.Fatalf("a rebuild kept the earlier run's packages: %q", f.file("checksums.txt"))
	}
	for name, envs := range map[string][]string{"no team id": {"!CASCADE_APPLE_TEAM_ID"}, "bad team id": {"CASCADE_APPLE_TEAM_ID=x"},
		"no out": {"CASCADE_APPLE_TEAM_ID=FAKETEAM00", "!CASCADE_WIDGET_OUT"}} {
		g := wrNew(t, wrIdentity)
		if g.do(wrBuild, false, envs); g.calls("xcodebuild", "")+g.calls("xcodegen", "") != 0 {
			t.Errorf("%s: archive ran before the refusal:\n%s", name, g.log())
		}
	}
}

// wrReal is a real-mode environment with fake, obviously synthetic credentials.
func wrReal(t *testing.T, keyID, issuer, keyBody string) []string {
	key := filepath.Join(t.TempDir(), "key.p8")
	ishMust(t, os.WriteFile(key, []byte(keyBody), 0o600))
	return []string{wrIdentity, "CASCADE_APPLE_NOTARY_KEY_ID=" + keyID, "CASCADE_APPLE_NOTARY_KEY_ISSUER=" + issuer, "CASCADE_APPLE_NOTARY_KEY_PATH=" + key}
}

func TestSignNotarizeRefusesMissingCredentials(t *testing.T) {
	creds := wrReal(t, "FAKEKEYID0", "fake-issuer", "fake key\n")
	for _, kv := range creds {
		f := wrNew(t, creds...)
		f.do(wrSig, true, nil, "submit") // a confirmed id: finish can refuse only on the credential
		signs, polls, drop := f.calls("codesign", ""), f.calls("notarytool", ""), []string{"!" + strings.SplitN(kv, "=", 2)[0]}
		f.do(wrSig, false, drop, "submit")
		f.do(wrSig, false, drop, "finish")
		if f.calls("codesign", "") != signs || f.calls("notarytool", "") != polls {
			t.Errorf("without %s a call signed or polled:\n%s", kv, f.log())
		}
	}
	f := wrNew(t, creds...)
	ishMust(t, os.WriteFile(filepath.Join(f.dir, "empty.p8"), nil, 0o600))
	f.do(wrSig, false, []string{"!CASCADE_APPLE_NOTARY_KEY_ID", "!CASCADE_APPLE_NOTARY_KEY_ISSUER", "!CASCADE_APPLE_NOTARY_KEY_PATH"}, "submit")
	f.do(wrSig, false, []string{"CASCADE_APPLE_NOTARY_KEY_PATH=" + filepath.Join(f.dir, "empty.p8")}, "submit")
	f.do(wrSig, false, []string{"CASCADE_APPLE_NOTARY_KEY_PATH=" + f.dir}, "submit") // a directory
	f.do(wrSig, false, []string{"CASCADE_APPLE_SIGNING_IDENTITY=-"}, "submit")
	if f.calls("codesign", "") != 0 {
		t.Errorf("a refused submit signed:\n%s", f.log())
	}
}

func TestSignNotarizeSkipModeMarks(t *testing.T) {
	f := wrNew(t, "CASCADE_NOTARY_SKIP=1", "CASCADE_APPLE_SIGNING_IDENTITY=-")
	f.do(wrSig, false, nil, "finish") // no skip-mode submit yet
	f.do(wrSig, true, nil, "submit")
	f.do(wrSig, true, nil, "finish")
	if f.file("notarization.txt") != "skipped\n" || f.calls("codesign", "")+f.calls("notarytool", "")+f.calls("stapler", "") != 0 {
		t.Fatalf("skip mode must mark %q and never sign or notarize:\n%s", f.file("notarization.txt"), f.log())
	}
	if out, err := exec.Command("bash", "-c", `cd "$1" && shasum -a 256 -c checksums.txt`, "_", f.out).CombinedOutput(); err != nil || strings.Count(string(out), ": OK") != 2 {
		t.Fatalf("checksums: %v\n%s", err, out)
	}
	creds := append(wrReal(t, "FAKEKEYID0", "fake-issuer", "fake key\n"), "!CASCADE_NOTARY_SKIP")
	f.do(wrSig, true, creds, "submit") // clears the skip-mode packages
	if strings.Contains(f.file("checksums.txt"), "CascadeMenuBar") || f.file("notarization.txt") != "" {
		t.Fatalf("a real submit kept the skip-mode packages: %q", f.file("checksums.txt"))
	}
	f.do(wrSig, true, nil, "submit") // back to skip mode voids the real submission id
	if f.do(wrSig, false, creds, "finish"); f.calls("notarytool", "info") != 0 {
		t.Fatalf("a real finish after a skip-mode submit polled:\n%s", f.log())
	}
}

func TestSignNotarizeCrashBetweenIntentAndConfirm(t *testing.T) {
	f := wrNew(t, wrReal(t, "FAKEKEYID0", "fake-issuer", "fake key\n")...)
	f.do(wrSig, true, nil, "submit") // an earlier confirmed id that must not count after the crash
	c := f.cmd(wrSig, []string{"SHIM_NOTARY=hang"}, "submit")
	ishMust(t, c.Start())
	for i := 0; i < 600 && f.file("../notary.pid") == ""; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	ishMust(t, c.Process.Kill())
	_ = c.Wait() // killed on purpose; the journal below is the assertion
	ishMust(t, exec.Command("kill", "-9", strings.TrimSpace(f.file("../notary.pid"))).Run())
	journal := f.file("notary-journal.log")
	if i := strings.LastIndex(journal, " intent "); i < 0 || strings.Contains(journal[i:], "submitted") {
		t.Fatalf("want a last intent line with no submission id after it:\n%s", journal)
	}
	polls := f.calls("notarytool", "info")
	f.do(wrSig, false, nil, "finish")
	if f.calls("stapler", "") != 0 || f.calls("notarytool", "info") != polls || f.file("checksums.txt") != "" {
		t.Fatalf("finish after a crash polled, stapled or packaged:\n%s", f.log())
	}
	f.do(wrSig, true, nil, "submit")
	f.do(wrSig, true, nil, "finish")
	if !strings.Contains(f.file("checksums.txt"), "CascadeMenuBar.dmg") || f.calls("codesign", "--force --options runtime --timestamp --preserve") != 6 ||
		f.calls("codesign", "--force --timestamp --sign Developer ID Application: Example (FAKETEAM00) "+f.out+"/.pkg/CascadeMenuBar.dmg") != 1 {
		t.Fatalf("a confirmed submission must sign app and extension hardened (three submits), sign the staged .dmg and package:\n%s", f.log())
	}
}

func TestSignNotarizeNoSecretEcho(t *testing.T) {
	keyID, issuer, body, tail := "CANARYKEYID7", "canary-issuer-5d1c-CANARYKEYID7", strings.Repeat("Q0FOQVJZS0VZTUFURVJJQUw", 3)[:64], "QUJDREVGR0g="
	f := wrNew(t, wrReal(t, keyID, issuer, "-----BEGIN "+"PRIVATE KEY-----\r\n"+body+"\r\n"+tail+"\r\n-----END "+"PRIVATE KEY-----\r\n")...)
	var seen strings.Builder
	for _, step := range []struct{ mode, fail string }{{"submit", "notarytool"}, {"submit", ""}, {"finish", "notarytool"}, {"finish", ""}} {
		seen.WriteString(f.do(wrSig, step.fail == "", []string{"SHIM_FAIL=" + step.fail}, step.mode))
	}
	files, err := exec.Command("find", f.out, "-type", "f", "-exec", "cat", "{}", "+").Output()
	ishMust(t, err)
	seen.Write(files)
	for _, canary := range []string{keyID, strings.ToUpper(issuer), "canary-issuer-5d1c", body[:40], tail, "PRIVATE KEY"} {
		if strings.Contains(seen.String(), canary) {
			t.Errorf("canary %q leaked into output or $OUT", canary)
		}
	}
	if !strings.Contains(seen.String(), "[redacted]") || !strings.Contains(f.file("notary-journal.log"), "submitted id="+wrSubmissionID) {
		t.Error("the failure output was not shown redacted, or the journal lacks the submission id")
	}
	for _, s := range wrScanned {
		if regexp.MustCompile(`(?m)set -[A-Za-z]*x|xtrace|^#!.*\s-[A-Za-z]*x`).MatchString(ishRead(t, filepath.Join(f.root, ".github", "scripts", s))) {
			t.Errorf("%s enables xtrace", s)
		}
	}
}

func TestSignNotarizeNoKeychainWrites(t *testing.T) {
	root := filecapModuleRoot(t)
	bad := regexp.MustCompile(`store-credentials|allowProvisioning|security +(import|add-|create-keychain|delete-|set-key-partition-list|unlock-keychain|default-keychain|list-keychains +-s)`)
	for _, l := range strings.Split(ishRead(t, filepath.Join(root, filepath.FromSlash(wrSeeded), "keychain-write.sh")), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") && !bad.MatchString(l) {
			t.Errorf("the keychain grep misses the seeded line %q", l)
		}
	}
	for _, s := range wrScanned {
		if m := bad.FindString(ishRead(t, filepath.Join(root, ".github", "scripts", s))); m != "" {
			t.Errorf("%s contains a keychain write: %q", s, m)
		}
	}
}

func TestSignNotarizeRefusesBadSigning(t *testing.T) {
	creds := wrReal(t, "FAKEKEYID0", "fake-issuer", "fake key\n")
	spoof := "SHIM_STATUS=x submitted id=11111111-2222-3333-4444-555555555555"
	for _, c := range [][3]string{{"codesign failure", "SHIM_FAIL=codesign", ""}, {"verify failure", "SHIM_FAIL=codesign:--verify", ""},
		{"stapler failure", "", "SHIM_FAIL=stapler"}, {"validate failure", "", "SHIM_FAIL=stapler:validate"},
		{"verify after staple", "", "SHIM_FAIL=codesign:--verify"}, {"Rejected", "", "SHIM_STATUS=Rejected"},
		{"Invalid", "", "SHIM_STATUS=Invalid"}, {"In Progress", "", "SHIM_STATUS=In Progress"}, {"spoofed status", "", spoof},
		{"dmg failure", "", "SHIM_FAIL=hdiutil"}, {"rebuild after submit", "CASCADE_APPLE_TEAM_ID=FAKETEAM00", ""}} {
		f := wrNew(t, creds...)
		f.do(wrSig, true, nil, "submit") // a confirmed id that a failed submit or a rebuild must void
		if c[0] == "rebuild after submit" {
			f.do(wrBuild, true, []string{c[1]})
		} else if c[1] != "" {
			f.do(wrSig, false, []string{c[1]}, "submit")
		}
		f.do(wrSig, false, []string{c[2]}, "finish")
		if f.file("checksums.txt")+f.file("CascadeMenuBar.zip")+f.file("CascadeMenuBar.dmg") != "" || strings.Contains(f.file("notary-journal.log"), "11111111-") ||
			(c[0] == "Rejected" && f.file("notary-log.json") == "") {
			t.Errorf("%s: checksum lines or a spoofed id written, or a rejection without its notary log", c[0])
		}
	}
}
