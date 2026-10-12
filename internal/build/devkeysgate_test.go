package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestDevkeysInBuildInfo(t *testing.T) {
	for _, tags := range []string{"devkeys", "pam,devkeys", "devkeys ", "", "pam", "devkeysx"} {
		t.Run(tags, func(t *testing.T) {
			bi := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "-tags", Value: tags}}}
			want := tags == "devkeys" || tags == "pam,devkeys" || tags == "devkeys "
			if got := DevkeysInBuildInfo(bi); got != want {
				t.Fatalf("tags %q: got %v, want %v", tags, got, want)
			}
		})
	}
	if DevkeysInBuildInfo(nil) || DevkeysInBuildInfo(&debug.BuildInfo{}) ||
		DevkeysInBuildInfo(&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "other", Value: "devkeys"}}}) {
		t.Fatal("absent -tags must not report devkeys")
	}
}

func TestCheckNoDevkeys(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckNoDevkeys(exe); (err != nil) != testBinaryHasDevkeys(t) {
		t.Fatalf("own test binary: %v", err)
	}
	for _, data := range [][]byte{[]byte("not a Go binary"), {0x7f, 'E', 'L', 'F'}} {
		path := filepath.Join(t.TempDir(), "artifact")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckNoDevkeys(path); err == nil || !strings.Contains(err.Error(), "build info") {
			t.Fatalf("unreadable build info accepted: %v", err)
		}
	}
	if err := CheckNoDevkeys(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing artifact accepted")
	}
}

func TestCheckNoDevkeysTaggedArtifact(t *testing.T) {
	path := buildDevkeysProbe(t)
	if err := CheckNoDevkeys(path); err == nil || !strings.Contains(err.Error(), "enables devkeys") {
		t.Fatalf("tagged artifact accepted: %v", err)
	}
}

// buildDevkeysProbe builds a zero-import program with the devkeys tag in its
// own temporary module and returns the artifact path.
func buildDevkeysProbe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":  "module devkeysprobe\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "probe")
	cmd := exec.Command("go", "build", "-tags", "devkeys", "-o", path, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build tagged probe: %v\n%s", err, out)
	}
	return path
}

func TestGoreleaserBuildsCarryNoDevkeysTag(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(coverageModuleRoot(t), ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkGoreleaserNoDevkeys(data); err != nil {
		t.Fatal(err)
	}
}

func TestGoreleaserNoDevkeysSeeded(t *testing.T) {
	const good = "builds:\n  - id: release\n    hooks:\n      post:\n        - 'go run ./internal/build/nodevkeys \"{{ .Path }}\"'\n"
	for _, ok := range []string{good, good + "    tool: go\n"} {
		if err := checkGoreleaserNoDevkeys([]byte(ok)); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{
		"tags":           good + "    tags: [pam, devkeys]\n",
		"flags":          good + "    flags: ['-tags=devkeys']\n",
		"env":            good + "    env: ['GOFLAGS=-tags=pam,devkeys']\n",
		"global-env":     "env: ['GOFLAGS=-tags=devkeys']\n" + good,
		"override-tags":  good + "    overrides:\n      - goos: linux\n        tags: [devkeys]\n",
		"override-flags": good + "    overrides:\n      - goos: linux\n        flags: ['-tags=devkeys']\n",
		"override-env":   good + "    overrides:\n      - goos: linux\n        env: ['GOFLAGS=-tags=devkeys']\n",
		"templated-tags": good + "    tags: ['{{ .Env.TAGS }}']\n",
		"templated-flag": good + "    flags: ['-tags={{ .Env.TAGS }}']\n",
		"templated-env":  good + "    env: ['GOFLAGS={{ .Env.FLAGS }}']\n",
		"templated-key":  good + "    env: ['{{ .Env.BUILD_ENV }}']\n",
		"global-key":     "env: ['{{ .Env.BUILD_ENV }}']\n" + good,
		"override-key":   good + "    overrides:\n      - goos: linux\n        env: ['{{ .Env.BUILD_ENV }}']\n",
		"tool-wrapper":   good + "    tool: ./devgo\n",
		"gobinary":       good + "    gobinary: ./devgo\n",
		"later-hook":     good + "        - cp other {{ .Path }}\n",
		"missing-hook":   "builds:\n  - id: release\n",
		"wrong-artifact": strings.ReplaceAll(good, "{{ .Path }}", "other"),
		"no-builds":      "builds: []\n",
		"malformed":      "builds: [",
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkGoreleaserNoDevkeys([]byte(data)); err == nil {
				t.Fatal("unsafe release configuration accepted")
			}
		})
	}
}

// testBinaryHasDevkeys reports whether the running test binary was built with
// the devkeys tag, so positive checks on it hold under `go test -tags devkeys`.
func testBinaryHasDevkeys(t *testing.T) bool {
	t.Helper()
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("test binary has no build info")
	}
	return DevkeysInBuildInfo(bi)
}
