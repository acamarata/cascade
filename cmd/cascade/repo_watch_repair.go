//go:build !windows

// Purpose: daemon-owned repair requests with an audit barrier before discard.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/acamarata/cascade/internal/repo"

	"github.com/acamarata/cascade/pkg/cascade"
)

// drifted hashes only the admission guard's scanned bytes.
func (t *repoTree) drifted(ctx context.Context, paths []string, prior map[string]repo.PathState) ([]string, error) {
	var drift []string
	for _, rel := range paths {
		a, err := repo.Admit(ctx, t.root, rel)
		if err != nil {
			return nil, err
		}
		if !a.Admitted {
			if _, exists := prior[rel]; exists || t.refused[rel] != a.Reason {
				drift = append(drift, rel)
				t.refused[rel] = a.Reason
			}
			continue
		}
		delete(t.refused, rel)
		sum := sha256.Sum256(a.Content)
		if hex.EncodeToString(sum[:]) != prior[rel].ContentSHA256 {
			drift = append(drift, rel)
		}
	}
	return drift, nil
}

type repoRepairRequest struct {
	repoID        string
	rebuild       bool
	beforeDiscard func(context.Context) error
	reply         chan repoRepairReply
}

type repoRepairReply struct {
	repaired int
	err      error
}

// repair sends work to the loop, then lets caller cancellation stop waiting only.
func (s *repoWatchSet) repair(ctx context.Context, id string, rebuild bool, before func(context.Context) error) (int, error) {
	unavailable := func() (int, error) {
		return 0, cascade.New(cascade.KindUnavailable, "context.repair: repository watcher is unavailable")
	}
	select {
	case <-s.stopped:
		return unavailable()
	default:
	}
	req := repoRepairRequest{repoID: id, rebuild: rebuild, beforeDiscard: before, reply: make(chan repoRepairReply, 1)}
	select {
	case <-ctx.Done():
		return 0, cascade.Wrap(cascade.KindCanceled, ctx.Err(), "context.repair: caller canceled")
	case <-s.w.Ctx.Done():
		return unavailable()
	case <-s.stopped:
		return unavailable()
	case s.requests <- req:
	}
	select {
	case out := <-req.reply:
		return out.repaired, out.err
	case <-ctx.Done():
		return 0, cascade.Wrap(cascade.KindCanceled, ctx.Err(), "context.repair: caller canceled")
	case <-s.w.Ctx.Done():
		return unavailable()
	case <-s.stopped:
		return unavailable()
	}
}

// serveRepair runs with the daemon context even when the sender hangs up.
func (s *repoWatchSet) serveRepair(ctx context.Context, req repoRepairRequest) {
	if err := s.start(ctx, req.repoID, true); err != nil {
		req.reply <- repoRepairReply{err: err}
		return
	}
	tree := s.live[req.repoID]
	if tree == nil {
		req.reply <- repoRepairReply{err: cascade.New(cascade.KindUnavailable, "context.repair: repository tree is unavailable")}
		return
	}
	var out repoRepairReply
	if req.rebuild {
		out.repaired, out.err = tree.rebuild(ctx, req.beforeDiscard)
	} else {
		report, err := tree.sweep(ctx)
		out.repaired, out.err = len(report.Applied), err
	}
	req.reply <- out
}

// rebuild holds the tree lock and keeps the watcher quiesced through the audit,
// deletion and complete sweep. An audit failure leaves every chunk and row intact.
func (t *repoTree) rebuild(ctx context.Context, beforeDiscard func(context.Context) error) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.watcher.Quiesce(ctx); err != nil {
		t.watcher.Resume()
		return 0, err
	}
	defer t.watcher.Resume()
	if beforeDiscard == nil {
		return 0, cascade.New(cascade.KindUnavailable, "context.repair: rebuild requires an audit appender")
	}
	if err := beforeDiscard(ctx); err != nil {
		return 0, err
	}
	paths, err := t.journal.Paths(ctx, t.id)
	if err != nil {
		return 0, err
	}
	for _, path := range paths {
		if err := t.leg.DeleteChunks(ctx, t.id, path.ChunkIDs); err != nil {
			return 0, err
		}
	}
	if err := t.journal.ForgetRepository(ctx, t.id); err != nil {
		return 0, err
	}
	t.refused = map[string]string{}
	report, err := t.sweepLocked(ctx)
	return len(report.Applied), err
}
