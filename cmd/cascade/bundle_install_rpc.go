//go:build !windows

// Purpose: the daemon side of bundle.install: an always-elevated verb whose
// verified witness must cover these exact params, and which stores one
// pending approval per repository that bundle.activate may consume once
// within ten minutes. Also holds the bundle service the four bundle verbs
// share (pending approvals in memory, installed records on disk).
// Inputs: params {repo_id, hash, policy_version}, decoded strictly; the
// rpc.VerifiedElevation witness in the handler's context.
// Outputs: {repo_id, hash, policy_version, expires_at}.
// Constraints: the elevation row (Always) and the owner-only access class are
// declared in this file; a call without a witness for bundle.install over
// these exact params bytes is KindElevationRequired and stores nothing.
// Records are keyed by repo_id (one file per repository under the data
// directory); a repository never reads or writes another's record. The
// approval lives only in memory, so a daemon restart drops it (fail closed).
// SPORT: cmd/cascade bundle install RPC (P1-HRN-16).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The bundle verbs, namespace harness.bundle.
const (
	bundleInstallMethod  = "bundle.install"
	bundleActivateMethod = "bundle.activate"
	bundleStatusMethod   = "bundle.status"
	bundleRollbackMethod = "bundle.rollback"
)

// bundleApprovalTTL bounds a pending approval and the rollback window after
// an activation.
const bundleApprovalTTL = 10 * time.Minute

// init adds the bundle.install row to the elevation table: every install
// needs a fresh attestation.
func init() {
	rpc.RegisterElevatedVerb(bundleInstallMethod, rpc.ElevationRule{Always: true})
}

var _ = rpc.DeclareAccess(bundleInstallMethod, rpc.AccessOwnerOnly)

var (
	bundleRepoIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	bundleHashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	bundlePolicyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// bundleAuditor is the slice of *audit.Log the rollback verb appends to.
type bundleAuditor interface {
	Append(ctx context.Context, event audit.Event) (audit.Record, error)
}

// bundlePending is one approval bundle.install stored for a repository.
type bundlePending struct {
	hash          string
	policyVersion string
	expiresAt     time.Time
}

// bundleRecord is the installed state of one repository.
type bundleRecord struct {
	RepoID        string    `json:"repo_id"`
	InstalledHash string    `json:"installed_hash"`
	PreviousHash  string    `json:"previous_hash"`
	PolicyVersion string    `json:"policy_version"`
	RolledBack    bool      `json:"rolled_back"`
	ActivatedAt   time.Time `json:"activated_at"`
}

// bundleService holds the state the four bundle verbs share. dir is the
// directory of the per-repository record files; an empty dir refuses every
// record read or write with KindUnavailable. A nil audit refuses rollback.
type bundleService struct {
	mu      sync.Mutex
	clock   runtime.Clock
	dir     string
	audit   bundleAuditor
	pending map[string]bundlePending
}

// newBundleService returns a service over dir, clock and auditor.
func newBundleService(dir string, clock runtime.Clock, auditor bundleAuditor) *bundleService {
	return &bundleService{clock: clock, dir: dir, audit: auditor, pending: map[string]bundlePending{}}
}

// bundleInstallParams is the bundle.install params.
type bundleInstallParams struct {
	RepoID        string `json:"repo_id"`
	Hash          string `json:"hash"`
	PolicyVersion string `json:"policy_version"`
}

// bundleInstallReply is the bundle.install reply.
type bundleInstallReply struct {
	RepoID        string    `json:"repo_id"`
	Hash          string    `json:"hash"`
	PolicyVersion string    `json:"policy_version"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// mountBundleVerbs registers the four verbs over s.
func mountBundleVerbs(reg *rpc.Registry, s *bundleService) {
	reg.Register(bundleInstallMethod, s.install)
	reg.Register(bundleActivateMethod, s.activate)
	reg.Register(bundleStatusMethod, s.status)
	reg.Register(bundleRollbackMethod, s.rollback)
}

// install checks the witness, validates the params and stores the pending
// approval.
func (s *bundleService) install(ctx context.Context, raw json.RawMessage) (any, error) {
	witness, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok || witness.Method() != bundleInstallMethod || witness.ParamsHash() != rpc.ParamsHash(raw) {
		return nil, cascade.New(cascade.KindElevationRequired, "bundle.install needs a verified elevation for this call")
	}
	var p bundleInstallParams
	if err := decodeBundleParams(raw, &p); err != nil {
		return nil, err
	}
	if err := validateBundleRef(p.RepoID, p.Hash); err != nil {
		return nil, err
	}
	if !bundlePolicyPattern.MatchString(p.PolicyVersion) {
		return nil, cascade.New(cascade.KindInvalidInput, "bundle: policy_version is malformed")
	}
	expires := s.approve(p.RepoID, p.Hash, p.PolicyVersion)
	return bundleInstallReply{RepoID: p.RepoID, Hash: p.Hash, PolicyVersion: p.PolicyVersion, ExpiresAt: expires}, nil
}

// approve stores the one pending approval for repoID, replacing any earlier
// one, and returns its expiry.
func (s *bundleService) approve(repoID, hash, policyVersion string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	expires := s.clock.Now().Add(bundleApprovalTTL)
	s.pending[repoID] = bundlePending{hash: hash, policyVersion: policyVersion, expiresAt: expires}
	return expires
}

// validateBundleRef checks a repo_id and, when non-empty, a hash.
func validateBundleRef(repoID, hash string) error {
	if !bundleRepoIDPattern.MatchString(repoID) {
		return cascade.New(cascade.KindInvalidInput, "bundle: repo_id is malformed")
	}
	if hash != "" && !bundleHashPattern.MatchString(hash) {
		return cascade.New(cascade.KindInvalidInput, "bundle: hash must be 64 lowercase hex characters")
	}
	return nil
}

// decodeBundleParams decodes raw into out, refusing unknown fields and
// trailing data.
func decodeBundleParams(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "bundle: params")
	}
	if _, err := dec.Token(); err != io.EOF {
		return cascade.New(cascade.KindInvalidInput, "bundle: params carry trailing data")
	}
	return nil
}
