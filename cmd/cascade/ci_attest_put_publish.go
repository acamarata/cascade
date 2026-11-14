//go:build !windows

package main

// Purpose: the controller side of ci.attest.put. For one accepted job nonce it
// signs a put per node that the config's [nodes.repos] lists for the fact's
// repo AND the record store knows, and sends it between an intent and a
// confirm or failed effect record, so a crash never leaves a put both sent
// and unrecorded.
// Inputs: the attestation row and acceptance fact for the nonce, the repos
// table, the node record store, the trust store, the controller signer.
// Outputs: one effect per (node, row) key; a failed send also files one
// attention item.
// Constraints: no heuristic or default target; a key that was begun is never
// begun again; the node store is idempotent, so recovery resends the same put.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	attestPutActor     = "ci-attest-put"
	attestPutKeyPrefix = "ci.attest.put:"
)

type attestPutRows interface {
	ByNonce(ctx context.Context, nonce string) ([]jobs.CiAttestationRow, error)
	ByID(ctx context.Context, id string) (jobs.CiAttestationRow, bool, error)
}

type attestPutNonces interface {
	AcceptedFor(ctx context.Context, nonce, commit, tree, checkpoint string) (jobs.AcceptanceFact, bool, error)
}

type attestPutRecords interface {
	Get(nodeID string) (nodes.DeviceRecord, error)
}

// nodeCall is one request to one node: the shape of the node channel's Call.
type nodeCall func(ctx context.Context, method string, params, out any) error

// attestPutPublisher builds and sends the puts. dial returns the channel to a node.
type attestPutPublisher struct {
	rows      attestPutRows
	nonces    attestPutNonces
	records   attestPutRecords
	trust     attest.TrustStore
	repos     func() (map[string][]string, error)
	sign      func(context.Context, []byte) ([]byte, error)
	dial      func(node string) nodeCall
	effects   audit.EffectWriter
	pending   func(context.Context) ([]audit.EffectHandle, error)
	attention func(ctx context.Context, sourceRef string)
	log       *slog.Logger
}

// attestPut is one built put and its effect identity.
type attestPut struct {
	node, key, hash string
	req             nodes.AttestPutRequest
}

func (p *attestPutPublisher) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// publish sends the puts for one accepted nonce. A nonce with no row or no
// fact, or a fact whose repo no configured and recorded node lists, sends
// nothing.
func (p *attestPutPublisher) publish(ctx context.Context, nonce string) error {
	if nonce == "" {
		return nil
	}
	rows, err := p.rows.ByNonce(ctx, nonce)
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		fact, ok, err := p.nonces.AcceptedFor(ctx, nonce, row.CommitSHA, row.TreeHash, row.CheckpointID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			p.logger().Info("ci attest put: no acceptance fact for the nonce", "row", row.ID)
			continue
		}
		targets, err := p.targets(fact.RepoID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, node := range targets {
			errs = append(errs, p.publishTo(ctx, node, row, fact))
		}
	}
	return p.firstErr(errs)
}

// targets lists, sorted, the nodes whose [nodes.repos] list holds repoID and
// whose record the store holds. An unknown node is skipped.
func (p *attestPutPublisher) targets(repoID string) ([]string, error) {
	repos, err := p.repos()
	if err != nil {
		return nil, err
	}
	var out []string
	for node, ids := range repos {
		if !slices.Contains(ids, repoID) {
			continue
		}
		if _, err := p.records.Get(node); err != nil {
			if cascade.HasKind(err, cascade.KindNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, node)
	}
	slices.Sort(out)
	return out, nil
}

// build makes the signed put for node from row and fact.
func (p *attestPutPublisher) build(ctx context.Context, node string, row jobs.CiAttestationRow, fact jobs.AcceptanceFact) (attestPut, error) {
	pub, err := p.trust.Lookup(ctx, row.NodeID, row.KeyID)
	if err != nil {
		return attestPut{}, err
	}
	wire, err := json.Marshal(attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON), Signature: row.Signature})
	if err != nil {
		return attestPut{}, cascade.Wrap(cascade.KindInternal, err, "ci attest put: encode the attestation")
	}
	req := nodes.AttestPutRequest{SignedAttestation: wire, SignerPubKeyB64: base64.StdEncoding.EncodeToString(pub),
		Acceptance: nodes.AttestPutAcceptance{CommitSHA: fact.Commit, TreeHash: fact.Tree, CheckpointID: fact.CheckpointID,
			Nonce: fact.Nonce, RepoID: fact.RepoID, JobSpecHash: fact.JobSpecHash, PolicyVersion: fact.PolicyVersion,
			PlanKinds: fact.PlanKinds, AcceptedAt: fact.AcceptedAt.UTC().Truncate(time.Second)}}
	payload, err := nodes.AttestPutPayload(req)
	if err != nil {
		return attestPut{}, err
	}
	sig, err := p.sign(ctx, payload)
	if err != nil {
		return attestPut{}, err
	}
	req.ControllerSig = base64.StdEncoding.EncodeToString(sig)
	sum := sha256.Sum256(payload)
	return attestPut{node: node, key: attestPutKeyPrefix + node + ":" + row.ID, hash: hex.EncodeToString(sum[:]), req: req}, nil
}

func (p *attestPutPublisher) publishTo(ctx context.Context, node string, row jobs.CiAttestationRow, fact jobs.AcceptanceFact) error {
	put, err := p.build(ctx, node, row, fact)
	if err != nil {
		return err
	}
	h, err := p.effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: attestPutActor,
		Action: nodes.AttestPutMethod, Key: put.key, ParamsHash: put.hash})
	if errors.Is(err, audit.ErrEffectExists) {
		return nil
	}
	if err != nil {
		if h.Key != "" {
			_, _ = p.effects.FailEffect(ctx, h, err.Error())
		}
		return err
	}
	return p.send(ctx, h, put)
}

// send runs the call for a begun intent and records its outcome.
func (p *attestPutPublisher) send(ctx context.Context, h audit.EffectHandle, put attestPut) error {
	var out nodes.AttestPutResult
	if err := p.dial(put.node)(ctx, nodes.AttestPutMethod, put.req, &out); err != nil {
		return p.fail(ctx, h, err)
	}
	if !out.Stored {
		return p.fail(ctx, h, cascade.New(cascade.KindUnavailable, "ci attest put: the node did not store the attestation"))
	}
	_, err := p.effects.ConfirmEffect(ctx, h, "stored")
	return err
}

func (p *attestPutPublisher) fail(ctx context.Context, h audit.EffectHandle, cause error) error {
	if _, err := p.effects.FailEffect(ctx, h, cause.Error()); err != nil {
		return cascade.Wrap(kindOr(err, cascade.KindUnavailable), err, "ci attest put: recording the failure of: "+cause.Error())
	}
	if p.attention != nil {
		p.attention(ctx, h.Key)
	}
	return cause
}

// recover finishes the puts whose intent has no outcome: it rebuilds each from
// its row and fact, resends it and confirms, or fails it with one attention
// item. Intents of other effect kinds are left alone.
func (p *attestPutPublisher) recover(ctx context.Context, pending []audit.EffectHandle) error {
	var errs []error
	for _, h := range pending {
		rest, ok := strings.CutPrefix(h.Key, attestPutKeyPrefix)
		if !ok {
			continue
		}
		node, rowID, ok := strings.Cut(rest, ":")
		if !ok {
			errs = append(errs, p.fail(ctx, h, cascade.New(cascade.KindInvalidInput, "ci attest put: malformed effect key")))
			continue
		}
		errs = append(errs, p.resend(ctx, h, node, rowID))
	}
	return p.firstErr(errs)
}

func (p *attestPutPublisher) resend(ctx context.Context, h audit.EffectHandle, node, rowID string) error {
	row, ok, err := p.rows.ByID(ctx, rowID)
	if err != nil {
		return err
	}
	if !ok {
		return p.fail(ctx, h, cascade.New(cascade.KindNotFound, "ci attest put: the attestation row is gone"))
	}
	fact, ok, err := p.nonces.AcceptedFor(ctx, row.Nonce, row.CommitSHA, row.TreeHash, row.CheckpointID)
	if err != nil {
		return err
	}
	if !ok {
		return p.fail(ctx, h, cascade.New(cascade.KindNotFound, "ci attest put: the acceptance fact is gone"))
	}
	put, err := p.build(ctx, node, row, fact)
	if err != nil {
		return p.fail(ctx, h, err)
	}
	return p.send(ctx, h, put)
}

// firstErr returns the first non-nil error and logs the rest, so one failed
// node never hides the others.
func (p *attestPutPublisher) firstErr(errs []error) error {
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if first == nil {
			first = err
			continue
		}
		p.logger().Warn("ci attest put: further failure", "error", err)
	}
	return first
}
