//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var _ = rpc.DeclareAccess("jobs.evidence.list", rpc.AccessOwnerOnly)
var _ = rpc.DeclareAccess("plugin.pbd.evidence", rpc.AccessOwnerOnly)

type evidenceReader interface {
	QueryAll(context.Context, string) ([]jobs.EvidenceRecord, error)
}

// EvidenceRow is a fixed-ceiling view of one evidence record.
type EvidenceRow struct {
	JobID              string                  `json:"job_id"`
	Seq                int64                   `json:"seq"`
	Kind               jobs.EvidenceKind       `json:"kind"`
	ProducerCapability jobs.ProducerCapability `json:"producer_capability"`
	AttemptID          string                  `json:"attempt_id"`
	CheckpointID       string                  `json:"checkpoint_id"`
	TreeHash           string                  `json:"tree_hash"`
	AttestorIdentity   string                  `json:"attestor_identity,omitempty"`
	Outcome            jobs.Outcome            `json:"outcome"`
	ResultHash         string                  `json:"result_hash,omitempty"`
	IdempotencyKey     string                  `json:"idempotency_key,omitempty"`
	ArtifactRef        string                  `json:"artifact_ref,omitempty"`
	StartedAt          string                  `json:"started_at"`
	EndedAt            string                  `json:"ended_at"`
	CreatedAt          string                  `json:"created_at"`
	DataClass          string                  `json:"data_class"`
	Redacted           bool                    `json:"redacted"`
}

func evidenceView(rec jobs.EvidenceRecord) EvidenceRow {
	rec, redacted := jobs.RedactForCeiling(rec, jobs.EvidenceReadCeiling)
	class, _ := provider.DataClassName(provider.TierOrLocalOnly(rec.DataClass))
	return EvidenceRow{
		JobID: rec.JobID, Seq: rec.Seq, Kind: rec.Kind, ProducerCapability: rec.ProducerCapability,
		AttemptID: rec.AttemptID, CheckpointID: rec.CheckpointID, TreeHash: rec.TreeHash,
		AttestorIdentity: rec.AttestorIdentity, Outcome: rec.Outcome, ResultHash: rec.ResultHash,
		IdempotencyKey: rec.IdempotencyKey, ArtifactRef: rec.ArtifactRef,
		StartedAt: rec.StartedAt.UTC().Format(time.RFC3339), EndedAt: rec.EndedAt.UTC().Format(time.RFC3339),
		CreatedAt: rec.CreatedAt.UTC().Format(time.RFC3339), DataClass: class, Redacted: redacted,
	}
}

func jobsEvidenceList(reader evidenceReader) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params struct {
			Ticket string `json:"ticket"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&params); err != nil || params.Ticket == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "evidence: non-empty ticket required")
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, cascade.New(cascade.KindInvalidInput, "evidence: invalid params")
		}
		records, err := reader.QueryAll(ctx, params.Ticket)
		if err != nil && err != jobs.ErrNoEvidence {
			return nil, err
		}
		rows := make([]EvidenceRow, 0, len(records))
		for _, rec := range records {
			rows = append(rows, evidenceView(rec))
		}
		return struct {
			Ticket string        `json:"ticket"`
			Rows   []EvidenceRow `json:"rows"`
		}{params.Ticket, rows}, nil
	}
}
