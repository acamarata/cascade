package jobs

// Purpose: persist only the tips supplied by an authorized trust fetch.
// Inputs: operator registration, remote identity, URL and received tips.
// Outputs: authenticated records independent of user-controlled git refs.
// Constraints: no git probes; replacement and signing share one transaction.
// SPORT: jobs/repository-trust-fetch/ADD.

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TrustFetch records one explicit trust fetch and exactly its received tips.
type TrustFetch struct {
	RemoteID  string
	URL       string
	FetchedAt time.Time
	Tips      []BranchTip
}

var trustRemotePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// RecordTrustFetch atomically replaces one remote's signed received tips.
func (r *RepoRegistry) RecordTrustFetch(ctx context.Context, rootPath, remoteID, url string, tips []BranchTip) error {
	if err := r.requireTrustSigner(); err != nil {
		return err
	}
	if !trustRemotePattern.MatchString(remoteID) || strings.Contains(remoteID, "..") || url == "" || !utf8.ValidString(url) || len(tips) == 0 {
		return cascade.New(cascade.KindInvalidInput, "trust fetch needs valid remote id, URL and non-empty tips")
	}
	if err := validateBranchTips(tips, cascade.KindInvalidInput); err != nil {
		return err
	}
	id, err := r.OperatorRegistered(ctx, rootPath)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return registryDBError(err)
	}
	defer func() { _ = tx.Rollback() }()
	stamp := r.clock.Now().UTC().Format(time.RFC3339Nano)
	if err := r.writeTrustFetch(ctx, tx, id, remoteID, url, stamp, tips); err != nil {
		return err
	}
	return registryDBError(tx.Commit())
}

func (r *RepoRegistry) writeTrustFetch(ctx context.Context, tx *sql.Tx, id, remote, url, stamp string, tips []BranchTip) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs_repo_trust_fetch(repo_id,remote_id,url,fetched_at) VALUES(?,?,?,?) ON CONFLICT(repo_id,remote_id) DO UPDATE SET url=excluded.url,fetched_at=excluded.fetched_at`, id, remote, url, stamp)
	if err != nil {
		return registryDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs_repo_trust_fetch_tip WHERE repo_id=? AND remote_id=?`, id, remote); err != nil {
		return registryDBError(err)
	}
	for _, t := range tips {
		sig, err := r.signTrust(ctx, "trust-fetch-tip", id, remote, url, stamp, t.Ref, t.Commit)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs_repo_trust_fetch_tip(repo_id,remote_id,ref,commit_id,sig) VALUES(?,?,?,?,?)`, id, remote, t.Ref, t.Commit, sig); err != nil {
			return registryDBError(err)
		}
	}
	return nil
}

// TrustFetches verifies persisted trust records without consulting git refs.
func (r *RepoRegistry) TrustFetches(ctx context.Context, rootPath string) ([]TrustFetch, error) {
	id, err := r.OperatorRegistered(ctx, rootPath)
	if err != nil {
		return nil, err
	}
	records, stamps, err := r.readTrustFetchParents(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].Tips, err = r.readTrustFetchTips(ctx, id, records[i], stamps[i])
		if err != nil {
			return nil, err
		}
	}
	return records, nil
}

func (r *RepoRegistry) readTrustFetchParents(ctx context.Context, id string) ([]TrustFetch, []string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT remote_id,url,fetched_at FROM jobs_repo_trust_fetch WHERE repo_id=? ORDER BY remote_id`, id)
	if err != nil {
		return nil, nil, registryDBError(err)
	}
	defer func() { _ = rows.Close() }()
	records, stamps := make([]TrustFetch, 0), make([]string, 0)
	for rows.Next() {
		var rec TrustFetch
		var stamp string
		if err := rows.Scan(&rec.RemoteID, &rec.URL, &stamp); err != nil {
			return nil, nil, registryDBError(err)
		}
		rec.FetchedAt, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, nil, cascade.Wrap(cascade.KindIntegrity, ErrTrustRecordForged, "trust_record_forged: jobs_repo_trust_fetch_tip timestamp")
		}
		records, stamps = append(records, rec), append(stamps, stamp)
	}
	return records, stamps, registryDBError(rows.Err())
}

func (r *RepoRegistry) readTrustFetchTips(ctx context.Context, id string, rec TrustFetch, stamp string) ([]BranchTip, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT ref,commit_id,sig FROM jobs_repo_trust_fetch_tip WHERE repo_id=? AND remote_id=? ORDER BY ref`, id, rec.RemoteID)
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
		if err := r.verifyTrust("jobs_repo_trust_fetch_tip", id+":"+rec.RemoteID+":"+t.Ref, sig, "trust-fetch-tip", id, rec.RemoteID, rec.URL, stamp, t.Ref, t.Commit); err != nil {
			return nil, err
		}
		if !validBranchTip(t) {
			return nil, cascade.Wrap(cascade.KindIntegrity, ErrTrustRecordForged, "trust_record_forged: jobs_repo_trust_fetch_tip invalid tip")
		}
		tips = append(tips, t)
	}
	if err := rows.Err(); err != nil {
		return nil, registryDBError(err)
	}
	if len(tips) == 0 {
		return nil, cascade.Wrap(cascade.KindIntegrity, ErrTrustRecordForged, "trust_record_forged: jobs_repo_trust_fetch_tip missing tips")
	}
	return tips, nil
}
