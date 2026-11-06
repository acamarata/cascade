package jobs

// Purpose: the node-side ci_attest_trust table: which ed25519 signer keys a
// node has vouched for, keyed by (node_id, key_id), and its store.
//
// Inputs: the node's ci.attest.put store, which vouches a signer key here,
// and the vouched trust store, which reads it back.
// Outputs: CiAttestTrustStore, VouchedKey, ciAttestTrustTableStep.
// Constraints: vouched_at is the caller's time, never a clock read; one key id
// maps to one public key across every node (a different key is KindConflict);
// a read error is KindUnavailable, never absence.

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableCiAttestTrust = "ci_attest_trust"

// VouchedKey is one ci_attest_trust row.
type VouchedKey struct {
	NodeID, KeyID, PubKeyB64 string
	VouchedAt                time.Time
}

// CiAttestTrustStore vouches and reads node signer keys.
type CiAttestTrustStore struct{ db *sql.DB }

// NewCiAttestTrustStore returns a store over db, which must carry the jobs
// schema (ApplyJobsSchema). A nil db is KindInvalidInput.
func NewCiAttestTrustStore(db *sql.DB) (*CiAttestTrustStore, error) {
	if db == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: NewCiAttestTrustStore requires a non-nil db")
	}
	return &CiAttestTrustStore{db: db}, nil
}

// ciAttestTrustTableStep creates ci_attest_trust with PRIMARY KEY(node_id,
// key_id); vouched_at is unix seconds.
func ciAttestTrustTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_attest_trust: signer keys each node has vouched for",
		Table: &migrate.TableDef{
			Name: tableCiAttestTrust,
			Columns: []migrate.ColumnDef{
				{Name: "node_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "key_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "pub_key_b64", Type: migrate.TypeText, NotNull: true},
				{Name: "vouched_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

func validVouch(nodeID, keyID, pubKeyB64 string, at time.Time) error {
	if nodeID == "" || keyID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: a vouch needs a node id and a key id")
	}
	raw, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return cascade.New(cascade.KindInvalidInput, "jobs: a vouched key must be base64 of a 32-byte ed25519 public key")
	}
	if at.IsZero() {
		return cascade.New(cascade.KindInvalidInput, "jobs: a vouch needs a non-zero time")
	}
	return nil
}

// Vouch records that nodeID vouches keyID as pubKeyB64 at at. Bad input is
// KindInvalidInput; a key id already vouched with a different public key, by
// any node, is KindConflict and keeps the first row. Repeating a vouch is a
// no-op that keeps the first vouched_at.
func (s *CiAttestTrustStore) Vouch(ctx context.Context, nodeID, keyID, pubKeyB64 string, at time.Time) error {
	if err := validVouch(nodeID, keyID, pubKeyB64, at); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: begin ci_attest_trust vouch")
	}
	defer func() { _ = tx.Rollback() }()
	var clash int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableCiAttestTrust+`
		WHERE key_id = ? AND pub_key_b64 <> ?`, keyID, pubKeyB64).Scan(&clash)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: read ci_attest_trust")
	}
	if clash > 0 {
		return cascade.New(cascade.KindConflict, "jobs: key "+keyID+" is already vouched with a different public key")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+tableCiAttestTrust+`
		(node_id, key_id, pub_key_b64, vouched_at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		nodeID, keyID, pubKeyB64, at.Unix()); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: write ci_attest_trust")
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: commit ci_attest_trust vouch")
	}
	return nil
}

// Lookup returns the public key nodeID vouched for keyID. An empty id is
// KindInvalidInput; no row for that exact pair is ("", false, nil); a read
// error is KindUnavailable, never absence.
func (s *CiAttestTrustStore) Lookup(ctx context.Context, nodeID, keyID string) (pubKeyB64 string, found bool, err error) {
	if nodeID == "" || keyID == "" {
		return "", false, cascade.New(cascade.KindInvalidInput, "jobs: a trust lookup needs a node id and a key id")
	}
	err = s.db.QueryRowContext(ctx, `SELECT pub_key_b64 FROM `+tableCiAttestTrust+`
		WHERE node_id = ? AND key_id = ?`, nodeID, keyID).Scan(&pubKeyB64)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read ci_attest_trust")
	}
	return pubKeyB64, true, nil
}

// List returns every vouched key ordered by node id, then key id. A read or
// scan error is KindUnavailable.
func (s *CiAttestTrustStore) List(ctx context.Context) ([]VouchedKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, key_id, pub_key_b64, vouched_at FROM `+tableCiAttestTrust+`
		ORDER BY node_id, key_id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list ci_attest_trust")
	}
	defer func() { _ = rows.Close() }()
	var out []VouchedKey
	for rows.Next() {
		var k VouchedKey
		var at int64
		if err := rows.Scan(&k.NodeID, &k.KeyID, &k.PubKeyB64, &at); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan ci_attest_trust")
		}
		k.VouchedAt = time.Unix(at, 0).UTC()
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list ci_attest_trust")
	}
	return out, nil
}
