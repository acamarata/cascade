package jobs

// Purpose: authenticate operator registration, baseline and trust-fetch rows.
// Inputs: installation signer bound by daemon composition, stored TEXT bytes.
// Outputs: verified records or an integrity refusal for the whole read.
// Constraints: no key provisioning; every string must retain valid UTF-8.
// SPORT: jobs/trust-record-signatures/ADD.

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// TrustRecordDomain separates installation trust records from other signatures.
const TrustRecordDomain = "cascade.trust.record/v1"

// ErrTrustRecordForged identifies unauthenticated persisted trust evidence.
var ErrTrustRecordForged = cascade.New(cascade.KindIntegrity, "trust_record_forged")

// WithTrustRecordSigner returns a copy bound to the installation signer.
func (r *RepoRegistry) WithTrustRecordSigner(s envelope.Signer) (*RepoRegistry, error) {
	if s == nil || len(s.PublicKey()) != ed25519.PublicKeySize {
		return nil, cascade.New(cascade.KindInvalidInput, "trust record signer requires an Ed25519 public key")
	}
	bound := *r
	bound.signer = s
	return &bound, nil
}

func (r *RepoRegistry) requireTrustSigner() error {
	if r.signer == nil {
		return cascade.New(cascade.KindPermissionDenied, "repository trust signer is not bound")
	}
	return nil
}

func trustRecordBytes(kind string, fields ...string) ([]byte, error) {
	for _, f := range fields {
		if !utf8.ValidString(f) {
			return nil, cascade.New(cascade.KindInvalidInput, "trust record contains invalid UTF-8")
		}
	}
	var record any
	switch kind {
	case "registration":
		record = struct{ T, RepoID, Root, RegisteredAt string }{kind, fields[0], fields[1], fields[2]}
	case "baseline":
		record = struct{ T, RepoID, RegisteredAt, Ref, Commit string }{kind, fields[0], fields[1], fields[2], fields[3]}
	case "trust-fetch-tip":
		record = struct{ T, RepoID, RemoteID, URL, FetchedAt, Ref, Commit string }{kind, fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]}
	}
	data, err := json.Marshal(record)
	return append([]byte(TrustRecordDomain+"\x1f"), data...), err
}

func (r *RepoRegistry) signTrust(ctx context.Context, kind string, fields ...string) ([]byte, error) {
	msg, err := trustRecordBytes(kind, fields...)
	if err != nil {
		return nil, err
	}
	sig, err := r.signer.Sign(ctx, msg)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(r.signer.PublicKey(), msg, sig) {
		return nil, cascade.New(cascade.KindIntegrity, "trust record signature does not verify")
	}
	return sig, nil
}

func (r *RepoRegistry) verifyTrust(table, key string, sig []byte, kind string, fields ...string) error {
	msg, err := trustRecordBytes(kind, fields...)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(r.signer.PublicKey(), msg, sig) {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTrustRecordForged, "trust_record_forged: %s %s", table, key)
	}
	return nil
}

var branchCommitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func validBranchTip(t BranchTip) bool {
	return utf8.ValidString(t.Ref) && strings.HasPrefix(t.Ref, "refs/heads/") && len(t.Ref) > len("refs/heads/") && !strings.Contains(t.Ref, "..") && branchCommitPattern.MatchString(t.Commit)
}

func validateBranchTips(tips []BranchTip, kind cascade.Kind) error {
	seen := map[string]bool{}
	for _, t := range tips {
		if !validBranchTip(t) || seen[t.Ref] {
			return cascade.Newf(kind, "invalid branch tip %q", t.Ref)
		}
		seen[t.Ref] = true
	}
	return nil
}

// Register records the first operator registration and immutable baseline.
func (r *RepoRegistry) Register(ctx context.Context, rootPath, remote string, tips BranchTipsFunc) (string, error) {
	if err := r.requireTrustSigner(); err != nil {
		return "", err
	}
	if tips == nil || !utf8.ValidString(rootPath) || !utf8.ValidString(remote) {
		return "", cascade.New(cascade.KindInvalidInput, "registration needs valid root, remote and tips function")
	}
	root, err := scope.CanonicalRepoRoot(ctx, rootPath, r.git)
	if err != nil {
		return "", err
	}
	baseline, err := tips(ctx, root)
	if err != nil {
		return "", err
	}
	if err := validateBranchTips(baseline, cascade.KindIntegrity); err != nil {
		return "", err
	}
	rec, err := r.graph.EnsureRepository(ctx, rootPath, remote, r.git)
	if err != nil {
		return "", err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", registryDBError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists string
	err = tx.QueryRowContext(ctx, `SELECT repo_id FROM jobs_repo_registration WHERE repo_id=?`, rec.ID).Scan(&exists)
	if err == nil {
		return rec.ID, nil
	}
	if err != sql.ErrNoRows {
		return "", registryDBError(err)
	}
	if err := r.insertRegistration(ctx, tx, rec.ID, root, baseline); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", registryDBError(err)
	}
	return rec.ID, nil
}

func (r *RepoRegistry) insertRegistration(ctx context.Context, tx *sql.Tx, id, root string, tips []BranchTip) error {
	stamp := r.clock.Now().UTC().Format(time.RFC3339Nano)
	sig, err := r.signTrust(ctx, "registration", id, root, stamp)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs_repo_registration(repo_id,root,registered_at,sig) VALUES(?,?,?,?)`, id, root, stamp, sig); err != nil {
		return registryDBError(err)
	}
	for _, t := range tips {
		sig, err := r.signTrust(ctx, "baseline", id, stamp, t.Ref, t.Commit)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs_repo_baseline(repo_id,ref,commit_id,sig) VALUES(?,?,?,?)`, id, t.Ref, t.Commit, sig); err != nil {
			return registryDBError(err)
		}
	}
	return nil
}

// Registration verifies the operator registration and every baseline row.
func (r *RepoRegistry) Registration(ctx context.Context, rootPath string) (RepoRegistration, error) {
	if err := r.requireTrustSigner(); err != nil {
		return RepoRegistration{}, err
	}
	id, err := r.ByPath(ctx, rootPath)
	if err != nil {
		return RepoRegistration{}, err
	}
	root, err := scope.CanonicalRepoRoot(ctx, rootPath, r.git)
	if err != nil {
		return RepoRegistration{}, err
	}
	var storedRoot, stamp string
	var sig []byte
	err = r.db.QueryRowContext(ctx, `SELECT root,registered_at,sig FROM jobs_repo_registration WHERE repo_id=?`, id).Scan(&storedRoot, &stamp, &sig)
	if err == sql.ErrNoRows {
		return RepoRegistration{RepoID: id}, nil
	}
	if err != nil {
		return RepoRegistration{}, registryDBError(err)
	}
	if err := r.verifyTrust("jobs_repo_registration", id, sig, "registration", id, root, stamp); err != nil {
		return RepoRegistration{}, err
	}
	if root != storedRoot {
		return RepoRegistration{}, cascade.Wrapf(cascade.KindIntegrity, ErrTrustRecordForged, "trust_record_forged: jobs_repo_registration %s root", id)
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return RepoRegistration{}, cascade.Wrap(cascade.KindIntegrity, err, "invalid registration timestamp")
	}
	tips, err := r.readBaseline(ctx, id, stamp)
	if err != nil {
		return RepoRegistration{}, err
	}
	return RepoRegistration{RepoID: id, OperatorRegistered: true, RegisteredAt: at, Baseline: tips}, nil
}

func (r *RepoRegistry) readBaseline(ctx context.Context, id, stamp string) ([]BranchTip, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT ref,commit_id,sig FROM jobs_repo_baseline WHERE repo_id=? ORDER BY ref`, id)
	if err != nil {
		return nil, registryDBError(err)
	}
	defer func() { _ = rows.Close() }()
	tips := make([]BranchTip, 0)
	for rows.Next() {
		var t BranchTip
		var sig []byte
		if err := rows.Scan(&t.Ref, &t.Commit, &sig); err != nil {
			return nil, registryDBError(err)
		}
		if err := r.verifyTrust("jobs_repo_baseline", id+":"+t.Ref, sig, "baseline", id, stamp, t.Ref, t.Commit); err != nil {
			return nil, err
		}
		if !validBranchTip(t) {
			return nil, cascade.Wrap(cascade.KindIntegrity, ErrTrustRecordForged, "trust_record_forged: jobs_repo_baseline invalid tip")
		}
		tips = append(tips, t)
	}
	return tips, registryDBError(rows.Err())
}

// OperatorRegistered requires a verified explicit operator registration.
func (r *RepoRegistry) OperatorRegistered(ctx context.Context, rootPath string) (string, error) {
	reg, err := r.Registration(ctx, rootPath)
	if err != nil {
		return "", err
	}
	if !reg.OperatorRegistered {
		return "", ErrRepoNotRegistered
	}
	return reg.RepoID, nil
}
