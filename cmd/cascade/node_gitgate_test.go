// Purpose: test receive-gate verbs with isolated homes, stores and elevation.

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/spf13/cobra"
)

func gitGateTestDeps(t *testing.T) nodeCLIDeps {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = runtime.GOOS
	deps.Gate = nil
	return deps
}

func executeGitGate(t *testing.T, deps nodeCLIDeps, args ...string) (string, error) {
	t.Helper()
	cmd := newNodeGitGateCmd(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

func TestRunGitGateInstall(t *testing.T) {
	deps := gitGateTestDeps(t)
	repo := filepath.Join(t.TempDir(), "missing.git")
	_, err := executeGitGate(t, deps, "install", "--repo", repo, "--repo-id", "unknown")
	if deps.GOOS == "windows" {
		if !errors.Is(err, ci.ErrWindowsNotSupported) {
			t.Fatalf("install: %v", err)
		}
	} else if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("install without elevation: %v", err)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatal("unauthorized install wrote repository")
	}
	if _, err := os.Stat(deps.Paths.DataDir()); !os.IsNotExist(err) {
		t.Fatal("unauthorized install wrote store")
	}
}

func TestGitGateInstallWindowsFirst(t *testing.T) {
	deps := gitGateTestDeps(t)
	deps.GOOS = "windows"
	_, err := executeGitGate(t, deps, "install")
	if !errors.Is(err, ci.ErrWindowsNotSupported) {
		t.Fatalf("windows refusal: %v", err)
	}
}

func gitGateBareRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "receive.git")
	gitT(t, filepath.Dir(repo), "init", "--bare", repo)
	return repo
}

func TestGitGateStatusReadOnly(t *testing.T) {
	deps := gitGateTestDeps(t)
	if deps.GOOS == "windows" {
		t.Skip(ci.ErrWindowsNotSupported.Error())
	}
	repo := gitGateBareRepo(t)
	out, err := executeGitGate(t, deps, "status", "--repo", repo, "--json")
	if err != nil || !strings.Contains(out, `"hook_installed": false`) {
		t.Fatalf("status: %s, %v", out, err)
	}
	if _, err := os.Stat(deps.Paths.DataDir()); !os.IsNotExist(err) {
		t.Fatal("status created data directory")
	}
	if _, err := ci.GateRepoIDFromConfig(context.Background(), repo); !errors.Is(err, ci.ErrGateRepoIDMissing) {
		t.Fatalf("status changed config: %v", err)
	}
}

func TestGitGateInstallFreshRegistry(t *testing.T) {
	deps := gitGateTestDeps(t)
	if deps.GOOS == "windows" {
		t.Skip(ci.ErrWindowsNotSupported.Error())
	}
	repo := gitGateBareRepo(t)
	err := installAuthorizedTestGate(t, deps, repo, "absent")
	if !errors.Is(err, jobs.ErrRepoNotRegistered) {
		t.Fatalf("unregistered install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "hooks", "pre-receive")); !os.IsNotExist(err) {
		t.Fatal("refused binding wrote hook")
	}
	if _, err := ci.GateRepoIDFromConfig(context.Background(), repo); !errors.Is(err, ci.ErrGateRepoIDMissing) {
		t.Fatalf("refused binding wrote config: %v", err)
	}
}

func TestGitGateInstallRegistered(t *testing.T) {
	deps := gitGateTestDeps(t)
	if deps.GOOS == "windows" {
		t.Skip(ci.ErrWindowsNotSupported.Error())
	}
	repo := gitGateBareRepo(t)
	db, _, err := openGitGateRegistry(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rec, err := scope.NewGraphStore(db).EnsureRepository(context.Background(), repo, "", contextgit.GitCommonDir)
	if err != nil {
		t.Fatal(err)
	}
	err = installAuthorizedTestGate(t, deps, repo, rec.ID)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	s, err := ci.GitGateStatus(repo, ci.MirrorState{})
	if err != nil || !s.HookInstalled || s.BinaryDrift || s.RepoID != rec.ID {
		t.Fatalf("installed: %+v, %v", s, err)
	}
}

func installAuthorizedTestGate(t *testing.T, deps nodeCLIDeps, repo, id string) error {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ci.ComputeSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	cmd := fakeCmdWithContext(t)
	var out bytes.Buffer
	cmd.SetOut(&out)
	return installGitGateAuthorized(cmd, deps, repo, id, bin, digest)
}

func TestGitGatePreReceiveMalformed(t *testing.T) {
	deps := gitGateTestDeps(t)
	if deps.GOOS == "windows" {
		t.Skip(ci.ErrWindowsNotSupported.Error())
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader("malformed\n"))
	if err := runGitGatePreReceive(cmd, deps); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("parse: %v", err)
	}
	if _, err := os.Stat(deps.Paths.DataDir()); !os.IsNotExist(err) {
		t.Fatal("malformed input wrote store")
	}
}

func TestGitGateMissingPolicyRefuses(t *testing.T) {
	deps := gitGateTestDeps(t)
	if deps.GOOS == "windows" {
		t.Skip(ci.ErrWindowsNotSupported.Error())
	}
	err := verifyGitGateTip(context.Background(), deps, nil, nil, "", ci.AttestWant{}, "")
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "policy-version") {
		t.Fatalf("missing policy: %v", err)
	}
}
