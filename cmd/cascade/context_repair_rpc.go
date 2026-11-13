//go:build !windows

// Purpose: owner-only repair RPC with a fail-closed audit barrier.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = rpc.DeclareAccess(contextRepairMethod, rpc.AccessOwnerOnly)

type contextRepairAppend func(context.Context, audit.Event) (audit.Record, error)

// openContextRepairAudit is the appender seam; opening happens before enqueue.
var openContextRepairAudit = func(w *daemonWiring) (contextRepairAppend, error) {
	log, err := openDaemonAuditLog(w.Store, w.Clock, nil, w.Paths)
	if err != nil {
		return nil, err
	}
	return log.Append, nil
}

func decodeRepairParams(raw json.RawMessage) (repairParams, error) {
	var p repairParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, cascade.Wrap(cascade.KindInvalidInput, err, "context.repair: params")
	}
	if _, err := dec.Token(); err != io.EOF {
		return p, cascade.New(cascade.KindInvalidInput, "context.repair: params carry trailing data")
	}
	if err := validateRepairReason(p.Rebuild, p.Reason); err != nil {
		return p, err
	}
	if p.Repo == "" || !filepath.IsAbs(p.Repo) {
		return p, cascade.New(cascade.KindInvalidInput, "context.repair: repo must be absolute")
	}
	return p, nil
}

func contextRepairHandler(w *daemonWiring, set *repoWatchSet) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeRepairParams(raw)
		if err != nil {
			return nil, err
		}
		if set == nil {
			return nil, cascade.New(cascade.KindUnavailable, "context.repair: repository watcher is unavailable")
		}
		inv, err := registerRepository(ctx, w.Paths, w.Clock, p.Repo)
		if err != nil {
			return nil, err
		}
		var before func(context.Context) error
		if p.Rebuild {
			before, err = repairAuditBarrier(w, inv.Repository.ID, p.Reason)
			if err != nil {
				return nil, err
			}
		}
		repaired, err := set.repair(ctx, inv.Repository.ID, p.Rebuild, before)
		if err != nil {
			return nil, err
		}
		return map[string]any{"repaired": repaired}, nil
	}
}

// repairAuditBarrier refuses an unusable appender before a request can be sent.
func repairAuditBarrier(w *daemonWiring, id, reason string) (func(context.Context) error, error) {
	appendEvent, err := openContextRepairAudit(w)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "context.repair: open rebuild audit")
	}
	if appendEvent == nil {
		return nil, cascade.New(cascade.KindUnavailable, "context.repair: rebuild audit appender is unavailable")
	}
	explain, err := json.Marshal(map[string]string{"repo_id": id, "reason": reason})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "context.repair: encode rebuild audit")
	}
	return func(ctx context.Context) error {
		_, err := appendEvent(ctx, audit.Event{Kind: audit.KindConfigReload, Actor: "local-owner",
			Action: "context.repair.rebuild", Explain: explain})
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "context.repair: append rebuild audit")
		}
		return nil
	}, nil
}
