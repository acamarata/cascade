//go:build !windows

// Purpose: bundle.activate, bundle.status and bundle.rollback over the bundle
// service (bundle_install_rpc.go). activate consumes the repository's one
// pending approval and records the installed hash; status reports the
// record; rollback marks it rolled back under its rules after one audit
// event.
// Inputs: params {repo_id, hash} (activate, rollback) or {repo_id} (status),
// decoded strictly.
// Outputs: the record {repo_id, installed_hash, previous_hash,
// policy_version, rolled_back}.
// Constraints: all three verbs are owner-only (declared here). An approval
// never survives one activate: any activate that finds one removes it before
// checking it, so a wrong hash or an expired approval is refused and the
// approval is gone. Rollback is refused for another hash, after the
// ten-minute window and a second time; it appends exactly one
// KindConfigReload event (Action bundle.rollback) before rolled_back is set,
// and an append error is returned with nothing changed.
// SPORT: cmd/cascade bundle activate/status/rollback RPCs (P1-HRN-16).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	_ = rpc.DeclareAccess(bundleActivateMethod, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(bundleStatusMethod, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(bundleRollbackMethod, rpc.AccessOwnerOnly)
)

// bundleRefParams is the {repo_id, hash} params of activate and rollback.
type bundleRefParams struct {
	RepoID string `json:"repo_id"`
	Hash   string `json:"hash"`
}

// bundleStatusParams is the {repo_id} params of status.
type bundleStatusParams struct {
	RepoID string `json:"repo_id"`
}

// bundleStatusReply is the record as the verbs report it.
type bundleStatusReply struct {
	RepoID        string `json:"repo_id"`
	InstalledHash string `json:"installed_hash"`
	PreviousHash  string `json:"previous_hash"`
	PolicyVersion string `json:"policy_version"`
	RolledBack    bool   `json:"rolled_back"`
}

func replyOf(r bundleRecord) bundleStatusReply {
	return bundleStatusReply{RepoID: r.RepoID, InstalledHash: r.InstalledHash, PreviousHash: r.PreviousHash,
		PolicyVersion: r.PolicyVersion, RolledBack: r.RolledBack}
}

// decodeBundleRef decodes and validates {repo_id, hash}; hash is required.
func decodeBundleRef(raw json.RawMessage) (bundleRefParams, error) {
	var p bundleRefParams
	if err := decodeBundleParams(raw, &p); err != nil {
		return p, err
	}
	if p.Hash == "" {
		return p, cascade.New(cascade.KindInvalidInput, "bundle: hash is required")
	}
	return p, validateBundleRef(p.RepoID, p.Hash)
}

// activate consumes the pending approval and records the installed hash.
func (s *bundleService) activate(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decodeBundleRef(raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pending[p.RepoID]
	delete(s.pending, p.RepoID)
	switch {
	case !ok:
		return nil, cascade.New(cascade.KindPermissionDenied, "bundle.activate: no pending approval for this repository")
	case pending.hash != p.Hash:
		return nil, cascade.New(cascade.KindPermissionDenied, "bundle.activate: the approval covers another hash")
	case !s.clock.Now().Before(pending.expiresAt):
		return nil, cascade.New(cascade.KindPermissionDenied, "bundle.activate: the approval has expired")
	}
	rec, err := s.load(p.RepoID)
	if err != nil && !cascade.HasKind(err, cascade.KindNotFound) {
		return nil, err
	}
	rec.RepoID = p.RepoID
	if rec.InstalledHash != "" {
		rec.PreviousHash = rec.InstalledHash
	}
	rec.InstalledHash, rec.PolicyVersion = p.Hash, pending.policyVersion
	rec.RolledBack, rec.ActivatedAt = false, s.clock.Now()
	if err := s.save(rec); err != nil {
		return nil, err
	}
	return replyOf(rec), nil
}

// status reports the repository's record, or KindNotFound.
func (s *bundleService) status(_ context.Context, raw json.RawMessage) (any, error) {
	var p bundleStatusParams
	if err := decodeBundleParams(raw, &p); err != nil {
		return nil, err
	}
	if err := validateBundleRef(p.RepoID, ""); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.load(p.RepoID)
	if err != nil {
		return nil, err
	}
	return replyOf(rec), nil
}

// rollback marks the installed hash rolled back after one audit event.
func (s *bundleService) rollback(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := decodeBundleRef(raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.load(p.RepoID)
	if err != nil {
		return nil, err
	}
	switch {
	case rec.RolledBack:
		return nil, cascade.New(cascade.KindPermissionDenied, "bundle.rollback: already rolled back")
	case rec.InstalledHash != p.Hash:
		return nil, cascade.New(cascade.KindPermissionDenied, "bundle.rollback: another hash is installed")
	case !s.clock.Now().Before(rec.ActivatedAt.Add(bundleApprovalTTL)):
		return nil, cascade.New(cascade.KindPermissionDenied, "bundle.rollback: the rollback window has closed")
	case s.audit == nil:
		return nil, cascade.New(cascade.KindUnavailable, "bundle.rollback: this daemon has no audit log")
	}
	explain, err := json.Marshal(map[string]string{"repo_id": p.RepoID, "hash": p.Hash})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "bundle.rollback: explain")
	}
	if _, err := s.audit.Append(ctx, audit.Event{Kind: audit.KindConfigReload, Actor: "owner",
		Action: bundleRollbackMethod, ParamsHash: audit.HashParams(raw), Verdict: "allow", Explain: explain}); err != nil {
		return nil, err
	}
	rec.RolledBack = true
	if err := s.save(rec); err != nil {
		return nil, err
	}
	return replyOf(rec), nil
}

// recordPath is the one file of repoID's record. repoID is validated by the
// caller, so it never names a path outside dir.
func (s *bundleService) recordPath(repoID string) (string, error) {
	if s.dir == "" {
		return "", cascade.New(cascade.KindUnavailable, "bundle: this daemon has no data directory")
	}
	return filepath.Join(s.dir, repoID+".json"), nil
}

// load reads repoID's record; a missing one is KindNotFound.
func (s *bundleService) load(repoID string) (bundleRecord, error) {
	path, err := s.recordPath(repoID)
	if err != nil {
		return bundleRecord{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return bundleRecord{}, cascade.New(cascade.KindNotFound, "bundle: no installed bundle for this repository")
	}
	if err != nil {
		return bundleRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "bundle: read record")
	}
	var rec bundleRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.RepoID != repoID {
		return bundleRecord{}, cascade.New(cascade.KindIntegrity, "bundle: record is corrupt")
	}
	return rec, nil
}

// save publishes rec atomically (0600, parent 0700).
func (s *bundleService) save(rec bundleRecord) error {
	path, err := s.recordPath(rec.RepoID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "bundle: encode record")
	}
	return runtime.WriteBytesAtomic(path, data)
}
