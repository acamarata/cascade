// Purpose: verify stored acceptance attestations against a real commit tree.
// The controller registry resolves repository identity when no override exists.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci/attest"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

type ciAttestVerifyDeps struct {
	Paths cruntime.PathProvider
	Clock cruntime.Clock
	// Git runs git with args in the repository being verified.
	Git func(ctx context.Context, args ...string) (string, error)
}

func productionCIAttestVerifyDeps() ciAttestVerifyDeps {
	return ciAttestVerifyDeps{Paths: lazyPaths{}, Clock: cruntime.NewSystemClock(), Git: gitInWorkingDir}
}

func gitInWorkingDir(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

type attestSubject struct{ commit, tree, repoID string }

type attestVerifyResult struct {
	Commit   string `json:"commit"`
	Tree     string `json:"tree"`
	RepoID   string `json:"repo_id"`
	Records  int    `json:"records"`
	Eligible int    `json:"eligible"`
}

func (r attestVerifyResult) String() string {
	return fmt.Sprintf("commit=%s tree=%s repo_id=%s records=%d eligible=%d", r.Commit, r.Tree, r.RepoID, r.Records, r.Eligible)
}

func addCIAttestVerifyFlags(ciCmd *cobra.Command) {
	verify, _, err := ciCmd.Find([]string{"attest", "verify"})
	if err != nil || verify == nil || verify.Name() != "verify" {
		return
	}
	verify.Flags().Bool("require", false, "exit 1 unless at least one eligible passing attestation exists")
	verify.Flags().String("repo-id", "", "the controller's repo id to bind (default: git config cascade.gate.repoId)")
}

func ciAttestVerify(deps ciAttestVerifyDeps) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return cascade.New(cascade.KindInvalidInput, "ci attest verify: exactly one commit is required")
		}
		require, _ := cmd.Flags().GetBool("require")
		repoFlag, _ := cmd.Flags().GetString("repo-id")
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		subj, err := resolveAttestSubject(ctx, deps, args[0], repoFlag)
		if err != nil {
			return err
		}
		res, err := verifyStoredAttestations(ctx, deps, subj)
		if err != nil {
			return err
		}
		// Exit 0 only for an eligible record, or (without --require) when no
		// record is stored at all. KindInternal is the taxonomy's exit 1,
		// the status the receive gate reads as "not attested".
		if res.Eligible == 0 && (require || res.Records > 0) {
			return cascade.Wrapf(cascade.KindInternal, attest.ErrUnverifiedAttestor,
				"ci attest verify: no eligible passing attestation for %s (%d stored)", subj.commit, res.Records)
		}
		return outputWriter(cmd).Result(res)
	}
}

func resolveAttestSubject(ctx context.Context, deps ciAttestVerifyDeps, rev, repoFlag string) (attestSubject, error) {
	if rev == "" || strings.HasPrefix(rev, "-") || strings.ContainsAny(rev, " \t\r\n\x00") {
		return attestSubject{}, cascade.New(cascade.KindInvalidInput, "ci attest verify: the commit must be a revision, not an option")
	}
	// --no-replace-objects: a refs/replace entry must not swap in another
	// commit's tree.
	commit, err := deps.Git(ctx, "--no-replace-objects", "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || commit == "" {
		return attestSubject{}, cascade.Newf(cascade.KindInvalidInput, "ci attest verify: %q is not a commit in this repository", rev)
	}
	tree, err := deps.Git(ctx, "--no-replace-objects", "rev-parse", "--verify", "--quiet", commit+"^{tree}")
	if err != nil || tree == "" {
		return attestSubject{}, cascade.Newf(cascade.KindInvalidInput, "ci attest verify: cannot resolve the tree of %s", commit)
	}
	repoID := strings.TrimSpace(repoFlag)
	if repoID == "" {
		repoID, _ = deps.Git(ctx, "config", "--get", "cascade.gate.repoId")
	}
	if repoID == "" {
		repoID, err = attestRegistryRepoID(ctx, deps)
		if err != nil {
			return attestSubject{}, err
		}
	}
	return attestSubject{commit: commit, tree: tree, repoID: repoID}, nil
}

func attestRegistryRepoID(ctx context.Context, deps ciAttestVerifyDeps) (string, error) {
	role, err := nodes.ResolveRole(nodes.NewFileControllerBindingBackend(deps.Paths.DataDir()))
	if err != nil {
		return "", err
	}
	if role != nodes.RoleController {
		return "", jobs.ErrRepoNotRegistered
	}
	root, err := deps.Git(ctx, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return "", jobs.ErrRepoNotRegistered
	}
	dir := deps.Paths.DataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "ci attest verify: create data directory")
	}
	path := filepath.Join(dir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "ci attest verify: open registry")
	}
	defer func() { _ = db.Close() }()
	if err = jobs.ApplyRepoRegistrySchema(ctx, db, migrate.SQLiteEmitter{}, deps.Clock, path, filepath.Join(dir, "backups")); err != nil {
		return "", err
	}
	reg, err := jobs.NewRepoRegistry(db, scope.NewGraphStore(db), contextgit.GitCommonDir, deps.Clock)
	if err != nil {
		return "", err
	}
	return reg.ByPath(ctx, root)
}

// verifyStoredAttestations checks every stored record for the subject. Any
// record that fails verification fails the verb.
func verifyStoredAttestations(ctx context.Context, deps ciAttestVerifyDeps, subj attestSubject) (attestVerifyResult, error) {
	res := attestVerifyResult{Commit: subj.commit, Tree: subj.tree, RepoID: subj.repoID}
	env, closeEnv, err := openAttestEnv(ctx, deps)
	if err != nil || env == nil {
		return res, err
	}
	defer closeEnv()
	rows, err := env.rows.ByCommitAndTree(ctx, subj.commit, subj.tree)
	if err != nil {
		return res, err
	}
	res.Records = len(rows)
	for _, row := range rows {
		ok, err := evaluateAttestationRow(ctx, env, subj, row, deps.Clock)
		if err != nil {
			return res, cascade.Wrapf(cascade.KindInternal, err, "ci attest verify: stored attestation %s does not verify", row.ID)
		}
		if ok {
			res.Eligible++
		}
	}
	return res, nil
}

func evaluateAttestationRow(ctx context.Context, env *attestEnv, subj attestSubject, row jobs.CiAttestationRow, clock cruntime.Clock) (bool, error) {
	sa := attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON), Signature: row.Signature}
	if err := row.CheckSigned(); err != nil {
		return false, err
	}
	if err := attest.VerifySignature(ctx, sa, subj.commit, subj.tree, env.trust, clock.Now()); err != nil {
		return false, err
	}
	rec, err := attest.DecodeCanonical(sa.CanonicalJSON)
	if err != nil {
		return false, err
	}
	if rec.RepoID != subj.repoID {
		return false, nil
	}
	fact, ok, err := env.nonces.AcceptedFor(ctx, rec.Nonce, subj.commit, subj.tree, rec.CheckpointID)
	if err != nil || !ok {
		return false, err
	}
	if fact.RepoID != subj.repoID || fact.JobSpecHash != rec.JobSpecHash || fact.PolicyVersion != rec.PolicyVersion ||
		fact.AcceptedAt.Before(rec.StartedAt) || !fact.AcceptedAt.Before(rec.ExpiresAt) {
		return false, nil
	}
	if !rec.Passing(fact.PlanKinds) {
		return false, nil
	}
	return attest.DeriveClass(sa, env.trust, nil).Eligible(), nil
}

type attestEnv struct {
	rows   *jobs.CiAttestationStore
	nonces *jobs.AttestationNonceStore
	trust  attest.TrustStore
}

// openAttestEnv opens cascade.db read-only and the node trust stores. A
// missing database, or one without the attestation tables, holds no
// records: it returns a nil env and no error.
func openAttestEnv(ctx context.Context, deps ciAttestVerifyDeps) (*attestEnv, func(), error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nil, nil, cascade.New(cascade.KindUnavailable, "ci attest verify: cannot resolve the cascade data directory")
	}
	db, err := openAttestDB(ctx, filepath.Join(dataDir, "cascade.db"))
	if err != nil || db == nil {
		return nil, nil, err
	}
	closeDB := func() { _ = db.Close() }
	trust, err := attestTrustForRole(db, dataDir, deps.Clock)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	rows, _ := jobs.NewCiAttestationStore(db)
	nonces, _ := jobs.NewAttestationNonceStore(db, deps.Clock)
	return &attestEnv{rows: rows, nonces: nonces, trust: trust}, closeDB, nil
}

// openAttestDB opens path read-only when it exists and carries both
// attestation tables; otherwise it returns nil and no error.
func openAttestDB(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci attest verify: stat cascade.db")
	}
	mode, err := readOnlyMode(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+mode+"&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci attest verify: open cascade.db")
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table'
		AND name IN ('ci_attestation', 'attestation_nonce')`).Scan(&n)
	if err != nil || n != 2 {
		_ = db.Close()
		if err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci attest verify: read the cascade.db schema")
		}
		return nil, nil
	}
	return db, nil
}

// attestTrustStore is the controller identity in front of the node record
// store; a controller with no saved identity verifies through the records.
func attestTrustStore(dataDir string, clock cruntime.Clock) (attest.TrustStore, error) {
	records := attest.NewRecordTrustStore(nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), clock))
	saved, ok, err := nodes.NewFileSelfIdentityBackend(dataDir).Load()
	if err != nil || !ok {
		return records, err
	}
	self, err := nodes.ValidateIdentity(saved.NodeID, saved.PubKeyB64)
	if err != nil {
		return nil, err
	}
	return attest.NewControllerTrustStore(self, records), nil
}

// readOnlyMode picks the read-only open that changes no file, as
// runtime.OpenReadOnlyStore does: immutable=1 with no write-ahead log files,
// mode=ro while a writer holds both, and "try again" while exactly one
// exists (an empty lone -wal counts as absent).
func readOnlyMode(path string) (string, error) {
	wal, werr := os.Stat(path + "-wal")
	_, serr := os.Stat(path + "-shm")
	if werr == nil && serr != nil && wal.Size() == 0 {
		werr = os.ErrNotExist
	}
	switch {
	case werr != nil && serr != nil:
		return "immutable=1", nil
	case (werr == nil) != (serr == nil):
		return "", cascade.New(cascade.KindUnavailable, "ci attest verify: cascade.db is changing (a writer is starting or stopping); try again")
	}
	return "mode=ro", nil
}
