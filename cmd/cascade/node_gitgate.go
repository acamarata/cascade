// Purpose: receive-gate CLI transport over the registry and CI verifier.
// Constraints: elevation precedes writes; missing policy or node membership
// readers refuse verification rather than weakening the gate.

package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gitGateCurrentPolicy is unset until a daemonless policy-version reader is
// available. An unset reader must refuse, never disable the policy check.
var gitGateCurrentPolicy func() (string, error)

func newNodeGitGateCmd(deps nodeCLIDeps) *cobra.Command {
	group := &cobra.Command{Use: "git-gate", Short: "Gate repository pushes on accepted attestations"}
	install := &cobra.Command{Use: "install", Short: "Install a pinned receive hook (elevated)", Args: usageArgs(cobra.NoArgs)}
	install.Annotations = map[string]string{"local": "true"}
	install.Flags().String("repo", ".", "Receiving repository")
	install.Flags().String("repo-id", "", "Registered repository id")
	install.RunE = func(cmd *cobra.Command, _ []string) error { return runGitGateInstall(cmd, deps) }
	status := &cobra.Command{Use: "status", Short: "Read receive-hook status", Args: usageArgs(cobra.NoArgs)}
	status.Flags().String("repo", ".", "Receiving repository")
	status.Flags().Bool("json", false, "Render JSON")
	status.RunE = func(cmd *cobra.Command, _ []string) error { return runGitGateStatus(cmd, deps) }
	receive := &cobra.Command{Use: "pre-receive", Hidden: true, Args: usageArgs(cobra.NoArgs)}
	receive.RunE = func(cmd *cobra.Command, _ []string) error { return runGitGatePreReceive(cmd, deps) }
	group.AddCommand(install, status, receive)
	return group
}

func runGitGateInstall(cmd *cobra.Command, deps nodeCLIDeps) error {
	if deps.GOOS == "windows" {
		return ci.ErrWindowsNotSupported
	}
	if err := requireElevation(cmd.Context(), deps.Gate, "node.git_gate_install"); err != nil {
		return err
	}
	repo, _ := cmd.Flags().GetString("repo")
	id, _ := cmd.Flags().GetString("repo-id")
	if id == "" {
		return cascade.New(cascade.KindInvalidInput, "git gate: --repo-id is required")
	}
	bin, err := os.Executable()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "git gate: executable path")
	}
	bin, err = filepath.EvalSymlinks(bin)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "git gate: resolve executable")
	}
	digest, err := ci.ComputeSHA256(bin)
	if err != nil {
		return err
	}
	return installGitGateAuthorized(cmd, deps, repo, id, bin, digest)
}

// installGitGateAuthorized runs only after the caller's elevation check.
func installGitGateAuthorized(cmd *cobra.Command, deps nodeCLIDeps, repo, id, bin, digest string) error {
	gateDir, err := receivingGateDir(cmd.Context(), repo)
	if err != nil {
		return err
	}
	db, reg, err := openGitGateRegistry(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := ci.BindGateRepoID(cmd.Context(), gateDir, id, reg); err != nil {
		return err
	}
	if err := ci.GitGateInstall(gateDir, id, bin, digest); err != nil {
		return err
	}
	return renderGitGateStatus(cmd, gateDir, reg)
}

func receivingGateDir(ctx context.Context, repo string) (string, error) {
	dirs, err := contextgit.GitCommonDir(ctx, repo)
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(dirs.CommonDir)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "git gate: canonical receiving directory")
	}
	return filepath.Clean(dir), nil
}

func openGitGateRegistry(ctx context.Context, deps nodeCLIDeps) (*sql.DB, *jobs.RepoRegistry, error) {
	dir := deps.Paths.DataDir()
	if dir == "" {
		return nil, nil, cascade.New(cascade.KindUnavailable, "git gate: no data directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "git gate: data directory")
	}
	path := filepath.Join(dir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "git gate: open registry")
	}
	err = jobs.ApplyRepoRegistrySchema(ctx, db, migrate.SQLiteEmitter{}, deps.Clock, path, filepath.Join(dir, "backups"))
	var reg *jobs.RepoRegistry
	if err == nil {
		reg, err = jobs.NewRepoRegistry(db, scope.NewGraphStore(db), contextgit.GitCommonDir, deps.Clock)
	}
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, reg, nil
}

func runGitGateStatus(cmd *cobra.Command, deps nodeCLIDeps) error {
	if deps.GOOS == "windows" {
		return ci.ErrWindowsNotSupported
	}
	repo, _ := cmd.Flags().GetString("repo")
	path := filepath.Join(deps.Paths.DataDir(), "cascade.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return renderGitGateStatus(cmd, repo, nil)
	}
	mode, err := readOnlyMode(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+mode)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "git gate: read registry")
	}
	defer func() { _ = db.Close() }()
	reg, err := jobs.NewRepoRegistry(db, scope.NewGraphStore(db), contextgit.GitCommonDir, deps.Clock)
	if err != nil {
		return err
	}
	return renderGitGateStatus(cmd, repo, reg)
}

func renderGitGateStatus(cmd *cobra.Command, repo string, reg *jobs.RepoRegistry) error {
	s, err := ci.GitGateStatus(repo, ci.MirrorState{})
	if err != nil {
		return err
	}
	if reg != nil && s.RepoID != "" {
		id, lookupErr := reg.GateRepoID(cmd.Context(), repo)
		if lookupErr != nil {
			return lookupErr
		}
		s.RepoIDBound = id == s.RepoID
	}
	return nodeOutputWriter(cmd).Result(s)
}

func runGitGatePreReceive(cmd *cobra.Command, deps nodeCLIDeps) error {
	if deps.GOOS == "windows" {
		return ci.ErrWindowsNotSupported
	}
	refs, err := ci.ParsePreReceiveStdin(cmd.InOrStdin())
	if err != nil {
		return err
	}
	// Git runs pre-receive in GIT_DIR. Resolve this process's repository;
	// never trust a configured path. Child git keeps quarantine variables.
	repo, err := os.Getwd()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "git gate: receiving cwd")
	}
	gateDir, err := receivingGateDir(cmd.Context(), repo)
	if err != nil {
		return err
	}
	db, reg, err := openGitGateRegistry(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	want, err := ci.ResolveGateWant(cmd.Context(), repo, ci.AttestWant{GateDir: gateDir}, reg)
	if err != nil {
		return err
	}
	return ci.CheckPush(cmd.Context(), repo, refs, func(commit string) error {
		return verifyGitGateTip(cmd.Context(), deps, db, reg, repo, want, commit)
	})
}

func verifyGitGateTip(ctx context.Context, deps nodeCLIDeps, db *sql.DB, reg *jobs.RepoRegistry, repo string, want ci.AttestWant, commit string) error {
	role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(deps.Paths.DataDir()))
	if err != nil {
		return err
	}
	if role == nodes.RoleNode {
		return cascade.New(cascade.KindPolicyDenied, "git gate: attestation verification requires the nodes.repos membership reader")
	}
	policy := gitGateCurrentPolicy
	// There is no daemonless policy-version reader in this build. Refuse
	// before looking at evidence, including when the tip has no rows.
	if policy == nil {
		return cascade.New(cascade.KindUnavailable, "git gate: current policy-version reader unavailable; attestation refused")
	}
	if _, err := policy(); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "--no-replace-objects", "rev-parse", "--verify", commit+"^{tree}").Output()
	if err != nil {
		return cascade.Wrap(cascade.KindPermissionDenied, err, "git gate: attestation tree")
	}
	want.CommitSHA, want.TreeHash = commit, strings.TrimSpace(string(out))
	rows, err := jobs.NewCiAttestationStore(db)
	if err != nil {
		return err
	}
	facts, err := jobs.NewAttestationNonceStore(db, deps.Clock)
	if err != nil {
		return err
	}
	trust, err := attestTrustForRole(db, deps.Paths.DataDir(), deps.Clock)
	if err != nil {
		return err
	}
	return ci.AttestVerify(ctx, want, true, rows, trust, facts, reg, policy, deps.Clock.Now())
}
