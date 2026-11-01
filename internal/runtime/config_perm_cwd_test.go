//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests for how the config walk reads the working directory: a relative
// config path is judged from the cwd the kernel opens it from (getcwd(2)),
// never from $PWD's spelling, which a link can make point elsewhere.

// TestConfigWalkRelativeDotDotFromLinkedCwd proves a relative config path
// whose ".." climbs out of a working directory reached through a link is
// judged where the kernel opens it (the link target's parent), not refused
// as changed because the cwd's spelling was cleaned lexically.
func TestConfigWalkRelativeDotDotFromLinkedCwd(t *testing.T) {
	isolateHome(t)
	base := t.TempDir()
	for _, dir := range []string{filepath.Join(base, "real", "sub"), filepath.Join(base, "spelled")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "real", "config.toml"), []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "spelled", "cwdlink")
	if err := os.Symlink(filepath.Join(base, "real", "sub"), cwd); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	assertAncestorVerdict(t, filepath.Join("..", "config.toml"), "", false)
}

// TestConfigWalkRelativeFromLinkedCwd proves a relative config path is
// judged from the physical working directory the kernel opens it from: a
// link that only spells the cwd (held by a 0777 directory) is never
// crossed by the open, so it neither refuses nor names that directory.
func TestConfigWalkRelativeFromLinkedCwd(t *testing.T) {
	isolateHome(t)
	base := t.TempDir()
	safe, loose := filepath.Join(base, "safe"), filepath.Join(base, "loose")
	for _, dir := range []string{safe, loose} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(safe, "config.toml"), []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(loose, "cwdlink")
	if err := os.Symlink(safe, cwd); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	assertAncestorVerdict(t, "config.toml", "", false)
}

// TestConfigPhysicalCwdIgnoresPWDLink proves the walk takes the working
// directory from the kernel: while a link that $PWD names is flipped
// between the real cwd and another directory, physicalPath(".") must keep
// returning the real cwd. Read through $PWD, it would follow the flip and
// let the walk judge a directory the open never crosses. One-sided: a
// correct tree never returns another directory, so it cannot flake.
func TestConfigPhysicalCwdIgnoresPWDLink(t *testing.T) {
	base := t.TempDir()
	realCwd, other := filepath.Join(base, "real"), filepath.Join(base, "other")
	for _, dir := range []string{realCwd, other} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	want, err := filepath.EvalSymlinks(realCwd)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "pwdlink")
	if err := os.Symlink(realCwd, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	done := make(chan struct{})
	flipped := make(chan struct{})
	go flipLink(link, []string{other, realCwd}, done, flipped)
	defer func() { close(done); <-flipped }()
	for i := 0; i < 20000; i++ {
		if got, err := physicalPath("."); err == nil && got != want {
			t.Fatalf("physicalPath(\".\") = %s after %d calls; want the kernel cwd %s", got, i, want)
		}
	}
}

// TestConfigWalkIgnoresFlippedPWDLink is the end-to-end form: the real cwd
// sits under a 0777 directory, and $PWD names a link flipped between it
// and a safe directory that also holds a config.toml. The walk must never
// judge the safe directory, so CheckConfigPermissions never returns ok.
func TestConfigWalkIgnoresFlippedPWDLink(t *testing.T) {
	isolateHome(t)
	base := t.TempDir()
	loose, safe := filepath.Join(base, "loose"), filepath.Join(base, "safe")
	realCwd := filepath.Join(loose, "x")
	for _, dir := range []string{realCwd, safe} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("schema_version = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "pwdlink")
	if err := os.Symlink(realCwd, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	done, flipped := make(chan struct{}), make(chan struct{})
	go flipLink(link, []string{safe, realCwd}, done, flipped)
	defer func() { close(done); <-flipped }()
	for i := 0; i < 5000; i++ {
		if got, err := CheckConfigPermissions("config.toml"); err == nil && got.Level == ConfigPermOK {
			t.Fatalf("CheckConfigPermissions = %+v after %d calls; the cwd sits under 0777 %s", got, i, loose)
		}
	}
}

// flipLink re-points link at each target in turn (atomic rename) until
// done closes, then closes flipped.
func flipLink(link string, targets []string, done <-chan struct{}, flipped chan<- struct{}) {
	defer close(flipped)
	tmp := link + ".next"
	for i := 0; ; i++ {
		select {
		case <-done:
			return
		default:
		}
		_ = os.Remove(tmp)
		if os.Symlink(targets[i%len(targets)], tmp) == nil {
			_ = os.Rename(tmp, link)
		}
	}
}
