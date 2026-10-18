// Purpose: tests for ProvisionRuntimeProfile: the 0700 per-spawn layout, the
// vendor auth file linked in place without being opened, and the refusal of
// every unsafe auth source.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-15.

package agents

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func skipWindowsProfile(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("profile modes carry no meaning on windows")
	}
}

// authFile writes a fake vendor auth file in a private directory and returns its path.
func authFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vendor")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(p, []byte(`{"token":"synthetic-auth-file-body"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func subAuth(src string) DriverAuth {
	return DriverAuth{Harness: "fake", Class: CredentialSubscription, AuthSource: src}
}

func wantEntitlement(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil || !errors.Is(err, provider.ErrEntitlement) || errors.Unwrap(err) != provider.ErrEntitlement {
		t.Fatalf("err = %v, want it to wrap provider.ErrEntitlement itself", err)
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), part) {
		t.Fatalf("err = %q, want KindPolicyDenied naming %q", err, part)
	}
}

func TestRuntimeProfileModes(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	base := filepath.Join(t.TempDir(), "profiles")
	b := fakeBinding("fake")
	key := DriverAuth{Harness: "fake", Class: CredentialAPIKey, KeyEnv: "SRC"}
	p, err := ProvisionRuntimeProfile(base, b, key, "job-1")
	if err != nil {
		t.Fatalf("ProvisionRuntimeProfile: %v", err)
	}
	job := filepath.Join(base, "fake", "job-1")
	if p.Home != filepath.Join(job, "home") || p.ConfigDir != filepath.Join(job, "home", "config") || p.Tmp != filepath.Join(job, "tmp") {
		t.Fatalf("profile = %+v, want the <base>/<harness>/<job>/{home,home/config,tmp} layout", p)
	}
	for _, d := range []string{base, filepath.Dir(job), job, p.Home, p.ConfigDir, p.Tmp} {
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s = %v, %v, want a 0700 directory", d, fi, err)
		}
	}
	if ents, _ := os.ReadDir(p.ConfigDir); len(ents) != 0 {
		t.Fatalf("a key-class profile linked %d entries into its config dir, want none", len(ents))
	}
	if p.Home == os.Getenv("HOME") {
		t.Fatal("the profile home is the daemon HOME")
	}
	// one profile per spawn: the same job id again is a conflict.
	if _, err := ProvisionRuntimeProfile(base, b, key, "job-1"); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second Provision(job-1) = %v, want KindConflict", err)
	}
	// a second job and a different harness share nothing.
	if q, err := ProvisionRuntimeProfile(base, b, key, "job-2"); err != nil || q.Home == p.Home {
		t.Fatalf("Provision(job-2) = %+v, %v", q, err)
	}
	// a harness directory that is looser than 0700 is refused, never reused or tightened.
	if err := os.Chmod(filepath.Dir(job), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionRuntimeProfile(base, b, key, "job-3"); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("0755 harness dir err = %v, want KindPermissionDenied", err)
	}
	if fi, err := os.Stat(filepath.Dir(job)); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("harness dir after refusal = %v, %v, want it left at 0755", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(job), "job-3")); !os.IsNotExist(err) {
		t.Fatalf("a refused provision left a job directory (err %v)", err)
	}
}

func TestRuntimeProfileRefusals(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	b := fakeBinding("fake")
	key := DriverAuth{Harness: "fake", Class: CredentialAPIKey, KeyEnv: "SRC"}
	base := filepath.Join(t.TempDir(), "profiles")
	for name, c := range map[string]struct {
		base, job string
		a         DriverAuth
		kind      cascade.Kind
	}{
		"relative base":       {"rel/base", "j", key, cascade.KindInvalidInput},
		"job id with slash":   {base, "a/b", key, cascade.KindInvalidInput},
		"job id dotdot":       {base, "..", key, cascade.KindInvalidInput},
		"empty job id":        {base, "", key, cascade.KindInvalidInput},
		"other harness creds": {base, "j", DriverAuth{Harness: "other", Class: CredentialAPIKey, KeyEnv: "S"}, cascade.KindInvalidInput},
		"unoffered class":     {base, "j", DriverAuth{Harness: "fake", Class: "oauth_token", KeyEnv: "S"}, cascade.KindInvalidInput},
	} {
		if _, err := ProvisionRuntimeProfile(c.base, b, c.a, c.job); !cascade.HasKind(err, c.kind) {
			t.Fatalf("%s: err = %v, want %v", name, err, c.kind)
		}
	}
	// a looser base (or harness dir) is refused, never tightened silently.
	loose := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionRuntimeProfile(loose, b, key, "j"); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("0755 base err = %v, want KindPermissionDenied", err)
	}
	// a base that is a symlink is not a real directory.
	realDir := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionRuntimeProfile(link, b, key, "j"); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("symlinked base err = %v, want KindPermissionDenied", err)
	}
}

// TestRuntimeProfileRefusesSharedHome: a daemon HOME or vendor config dir
// that names the new profile's home is refused and the job directory removed.
func TestRuntimeProfileRefusesSharedHome(t *testing.T) {
	skipWindowsProfile(t)
	b := fakeBinding("fake")
	key := DriverAuth{Harness: "fake", Class: CredentialAPIKey, KeyEnv: "SRC"}
	for _, name := range []string{"HOME", b.ConfigDirVar} {
		isolateHome(t)
		base := filepath.Join(t.TempDir(), "profiles")
		job := filepath.Join(base, "fake", "j")
		t.Setenv(name, filepath.Join(job, "home"))
		// the env var names where the new profile's home will be; the post-create check sees it resolve to the same place.
		_, err := ProvisionRuntimeProfile(base, b, key, "j")
		if !cascade.HasKind(err, cascade.KindPermissionDenied) || !strings.Contains(err.Error(), "own home") {
			t.Fatalf("%s names the profile home: err = %v, want KindPermissionDenied", name, err)
		}
		if _, statErr := os.Lstat(job); !os.IsNotExist(statErr) {
			t.Fatalf("%s: the refused job directory was left behind (%v)", name, statErr)
		}
	}
}

func TestProvisionLinksAuthSourceInPlace(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	src := authFile(t, 0o000) // unreadable: any attempt to open the content would fail
	b := fakeBinding("fake")
	p, err := ProvisionRuntimeProfile(filepath.Join(t.TempDir(), "profiles"), b, subAuth(src), "job-1")
	if err != nil {
		t.Fatalf("Provision(0000 auth file): %v, want it linked without being opened", err)
	}
	link := filepath.Join(p.ConfigDir, "auth.json")
	want, err := filepath.EvalSymlinks(filepath.Dir(src)) // the link names the resolved path, so no ancestor link can be re-pointed
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(want, filepath.Base(src))
	if got, err := os.Readlink(link); err != nil || got != want {
		t.Fatalf("Readlink(%s) = %q, %v, want the resolved source path %q", link, got, err, want)
	}
	if fi, err := os.Lstat(src); err != nil || fi.Mode().Perm() != 0 || fi.Size() == 0 {
		t.Fatalf("the source changed: %v, %v", fi, err)
	}
	if ents, _ := os.ReadDir(p.ConfigDir); len(ents) != 1 {
		t.Fatalf("config dir holds %d entries, want only the link", len(ents))
	}
}

func TestProvisionRefusesUnsafeAuthSource(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	b := fakeBinding("fake")
	base := filepath.Join(t.TempDir(), "profiles")
	try := func(name, src, part string) {
		t.Helper()
		job := strings.NewReplacer(" ", "-", "/", "-").Replace(name)
		_, err := ProvisionRuntimeProfile(base, b, subAuth(src), job)
		wantEntitlement(t, err, part)
		if _, statErr := os.Lstat(filepath.Join(base, "fake", job)); !os.IsNotExist(statErr) {
			t.Fatalf("%s: a refused spawn left its profile behind (%v)", name, statErr)
		}
	}
	grp := authFile(t, 0o660)
	if err := os.Chmod(grp, 0o660); err != nil {
		t.Fatal(err)
	}
	try("group writable", grp, "not group or world writable")
	oth := authFile(t, 0o602)
	try("world writable", oth, "not group or world writable")
	good := authFile(t, 0o600)
	lnk := filepath.Join(filepath.Dir(good), "alias.json")
	if err := os.Symlink(good, lnk); err != nil {
		t.Fatal(err)
	}
	try("symlink", lnk, "regular file")
	try("directory", filepath.Dir(good), "regular file")
	try("missing", filepath.Join(filepath.Dir(good), "gone.json"), "not usable")
	open := authFile(t, 0o600)
	if err := os.Chmod(filepath.Dir(open), 0o777); err != nil {
		t.Fatal(err)
	}
	try("writable parent", open, "writable by the daemon user alone")
	if os.Geteuid() != 0 {
		if _, err := os.Lstat("/etc/hosts"); err == nil {
			try("other uid", "/etc/hosts", "owned by the daemon user")
		}
	}
}
