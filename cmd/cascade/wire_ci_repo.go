//go:build !windows

// Purpose: compose the owner-only, elevated repository registration writer.
// The controller installation signer authenticates the immutable baseline.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"github.com/acamarata/cascade/internal/ci"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = registerDaemonWiring(daemonRegistration{Name: "ci-repo", Phase: phaseNamespaces, Order: 46, Wire: wireCiRepoRPC})
var _ = rpc.DeclareAccess("ci.repo_register", rpc.AccessOwnerOnly)

func wireCiRepoRPC(w *daemonWiring) error {
	w.Registry.Register("ci.repo_register", func(ctx context.Context, raw json.RawMessage) (any, error) {
		witness, ok := rpc.VerifiedElevationFrom(ctx)
		if !ok || witness.Method() != "ci.repo_register" {
			return nil, cascade.New(cascade.KindElevationRequired, "ci.repo_register requires local elevation")
		}
		var params ciRepoRegisterParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "ci.repo_register parameters")
		}
		return registerCIRepo(ctx, w, params)
	})
	return nil
}

func openCIRepoRegistry(ctx context.Context, w *daemonWiring) (*jobs.RepoRegistry, func(), error) {
	if err := os.MkdirAll(w.Paths.DataDir(), 0700); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "ci repo register: create data directory")
	}
	path := filepath.Join(w.Paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "ci repo register: open registry")
	}
	closeDB := func() { _ = db.Close() }
	if err = jobs.ApplyRepoRegistrySchema(ctx, db, migrate.SQLiteEmitter{}, w.Clock, path, filepath.Join(w.Paths.DataDir(), "backups")); err != nil {
		closeDB()
		return nil, nil, err
	}
	reg, err := jobs.NewRepoRegistry(db, scope.NewGraphStore(db), contextgit.GitCommonDir, w.Clock)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	return reg, closeDB, nil
}

func registerCIRepo(ctx context.Context, w *daemonWiring, p ciRepoRegisterParams) (any, error) {
	signer, err := productionEnvelopeSigner(ctx, w.Paths)
	if err != nil {
		return nil, err
	}
	reg, closeDB, err := openCIRepoRegistry(ctx, w)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	reg, err = reg.WithTrustRecordSigner(signer)
	if err != nil {
		return nil, err
	}
	b := ci.RegistrationBaseline(ci.BaselinePolicy{Explicit: p.Baseline})
	id, err := reg.Register(ctx, p.Path, contextgit.GitRemoteURL(ctx, p.Path), b.Tips)
	if err != nil {
		return nil, err
	}
	stored, err := reg.Registration(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	reply := ciRepoRegisterReply{RepoID: id, RegisteredAt: stored.RegisteredAt, Detached: b.Detached(), Baseline: []ciRepoTip{}, Excluded: []ciRepoExcludedTip{}}
	for _, t := range stored.Baseline {
		reply.Baseline = append(reply.Baseline, ciRepoTip{t.Ref, t.Commit})
	}
	if len(p.Baseline) > 0 {
		requested, err := b.Tips(ctx, p.Path)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(requested, stored.Baseline) {
			return nil, &rpc.ErrorObject{Code: cascade.RPCCodeConflict, Message: "conflict: ci repo register: baseline already recorded", Data: map[string]any{"kind": "conflict", "registration": reply}}
		}
	}
	for _, t := range b.Excluded() {
		reply.Excluded = append(reply.Excluded, ciRepoExcludedTip{t.Tip.Ref, t.Tip.Commit, t.Reason})
	}
	return reply, nil
}
