package runtime

// Purpose: TestCIConfig* proves [ci.policy] and [ci.local] register via
// Load (P1-E25-W5-S51-T5, task 1): defaults, a valid explicit value, and
// every error-path task 1 names (invalid commands, malformed patterns,
// unrecognised keys) -- reusing config_widget_test.go's
// loadWidgetTestConfig/writeConfigFile/fakeEnviron helper shape.

import (
	"context"
	"strings"
	"testing"
)

func loadCITestConfig(t *testing.T, toml string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := writeConfigFile(t, dir, toml)
	return Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
}

func TestCIConfigDefaults(t *testing.T) {
	cfg, err := loadCITestConfig(t, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CIPolicy.PrivateRepos) != 0 {
		t.Errorf("PrivateRepos = %v, want empty", cfg.CIPolicy.PrivateRepos)
	}
	if cfg.CILocal.TimeoutSeconds != defaultCITimeoutSeconds {
		t.Errorf("TimeoutSeconds = %d, want default %d", cfg.CILocal.TimeoutSeconds, defaultCITimeoutSeconds)
	}
	if cfg.CILocal.Lint != nil || cfg.CILocal.Test != nil || cfg.CILocal.Build != nil {
		t.Error("Lint/Test/Build must default to nil (repo-type auto-detection owns the default)")
	}
}

func TestCIConfigPolicyExplicit(t *testing.T) {
	cfg, err := loadCITestConfig(t, "[ci.policy.repos]\nprivate = [\"acamarata/secret-repo\", \"acamarata/other\"]\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"acamarata/secret-repo", "acamarata/other"}
	if len(cfg.CIPolicy.PrivateRepos) != len(want) {
		t.Fatalf("PrivateRepos = %v, want %v", cfg.CIPolicy.PrivateRepos, want)
	}
	for i, p := range want {
		if cfg.CIPolicy.PrivateRepos[i] != p {
			t.Errorf("PrivateRepos[%d] = %q, want %q", i, cfg.CIPolicy.PrivateRepos[i], p)
		}
	}
}

func TestCIConfigLocalExplicit(t *testing.T) {
	toml := "[ci.local]\nlint = [\"golangci-lint run ./...\"]\ntest = [\"go test ./...\"]\nbuild = [\"go build ./...\"]\ntimeout_seconds = 60\n"
	cfg, err := loadCITestConfig(t, toml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CILocal.Lint) != 1 || cfg.CILocal.Lint[0] != "golangci-lint run ./..." {
		t.Errorf("Lint = %v", cfg.CILocal.Lint)
	}
	if cfg.CILocal.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d, want 60", cfg.CILocal.TimeoutSeconds)
	}
}

func TestCIConfigPolicyMalformedPattern(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.policy.repos]\nprivate = [\"not-a-pattern\"]\n")
	if err == nil {
		t.Fatal("expected an error for a private-repo pattern with no \"owner/repo\" slash")
	}
}

func TestCIConfigPolicyNonStringPattern(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.policy.repos]\nprivate = [1]\n")
	if err == nil {
		t.Fatal("expected an error for a non-string private-repo pattern")
	}
}

func TestCIConfigPolicyUnknownKey(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.policy]\nbogus = true\n")
	if err == nil {
		t.Fatal("expected an error for an unrecognised [ci.policy] key")
	}
}

func TestCIConfigLocalInvalidCommand(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.local]\nlint = [\"\"]\n")
	if err == nil {
		t.Fatal("expected an error for a blank [ci.local].lint command")
	}
}

func TestCIConfigLocalNonArrayCommand(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.local]\ntest = \"go test ./...\"\n")
	if err == nil {
		t.Fatal("expected an error when [ci.local].test is not an array")
	}
}

func TestCIConfigLocalUnknownKey(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.local]\nbogus = 1\n")
	if err == nil {
		t.Fatal("expected an error for an unrecognised [ci.local] key")
	}
}

func TestCIConfigLocalInvalidTimeout(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.local]\ntimeout_seconds = -1\n")
	if err == nil {
		t.Fatal("expected an error for a non-positive timeout_seconds")
	}
}

func TestCIConfigPolicyReposNotTable(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.policy]\nrepos = \"nope\"\n")
	if err == nil {
		t.Fatal("expected an error when [ci.policy].repos is not a table")
	}
}

// TestCIConfigPolicyGlobPatternAccepted proves the entries are GLOB
// patterns: "acamarata/*" is a valid value, not a literal repository name
// that happens to contain a star.
func TestCIConfigPolicyGlobPatternAccepted(t *testing.T) {
	cfg, err := loadCITestConfig(t, "[ci.policy.repos]\nprivate = [\"acamarata/*\", \"*/internal-tooling\"]\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CIPolicy.PrivateRepos) != 2 {
		t.Fatalf("PrivateRepos = %v, want both glob patterns preserved", cfg.CIPolicy.PrivateRepos)
	}
}

// TestCIConfigPolicyUnparseablePatternRefused proves an unparseable glob is
// a LOAD-time error naming the key. Before the glob reading, a pattern that
// could never match was simply carried through and silently protected
// nothing -- a routing hole that failed OPEN.
func TestCIConfigPolicyUnparseablePatternRefused(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.policy.repos]\nprivate = [\"acamarata/[unclosed\"]\n")
	if err == nil {
		t.Fatal("expected an error for an unparseable owner/repo glob pattern")
	}
	if !strings.Contains(err.Error(), "ci.policy.repos.private[0]") {
		t.Errorf("error = %q, want it to name the offending key", err.Error())
	}
}

func TestCIConfigLocalEnvKeys(t *testing.T) {
	cfg, err := loadCITestConfig(t, "[ci.local]\nlint = [\"l\"]\nenv = [\"MY_BUILD_FLAG\", \"NPM_TOKEN\"]\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"MY_BUILD_FLAG", "NPM_TOKEN"}
	if len(cfg.CILocal.EnvKeys) != len(want) {
		t.Fatalf("EnvKeys = %v, want %v", cfg.CILocal.EnvKeys, want)
	}
	for i, k := range want {
		if cfg.CILocal.EnvKeys[i] != k {
			t.Errorf("EnvKeys[%d] = %q, want %q", i, cfg.CILocal.EnvKeys[i], k)
		}
	}
}

// TestCIConfigLocalEnvRejectsNameValuePairs proves env takes NAMES only: a
// "K=V" entry is refused rather than quietly treated as a variable called
// "K=V", which would put a credential in config.toml and never reach a step.
func TestCIConfigLocalEnvRejectsNameValuePairs(t *testing.T) {
	_, err := loadCITestConfig(t, "[ci.local]\nenv = [\"NPM_TOKEN=secret\"]\n")
	if err == nil {
		t.Fatal("expected an error for a NAME=VALUE entry in [ci.local].env")
	}
}

func TestCIConfigLocalEnvRejectsNonArray(t *testing.T) {
	if _, err := loadCITestConfig(t, "[ci.local]\nenv = \"NPM_TOKEN\"\n"); err == nil {
		t.Fatal("expected an error when [ci.local].env is not an array")
	}
}

func TestCIConfigLocalEnvRejectsBlankName(t *testing.T) {
	if _, err := loadCITestConfig(t, "[ci.local]\nenv = [\"  \"]\n"); err == nil {
		t.Fatal("expected an error for a blank [ci.local].env name")
	}
}
