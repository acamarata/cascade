//go:build !windows

package main

// Purpose: the "ci-attest-put" daemon registration. On each jobs.accepted
// event it publishes ci.attest.put to the nodes [nodes.repos] names for the
// accepted repo, in effect records. Until the node channel is composed the
// call is unboundNodeChannel, which does no I/O and reports KindUnavailable,
// so every put ends as a failed effect and one attention item.
// Inputs: w.Deps.Fleet, w.Bus, w.Deps.Reloader, the data dir's node records.
// Outputs: effect records and attention items; no registration without them.
// Constraints: [nodes] is read only through nodes.LoadSection.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	attestPutSubsystem = "ci-attest-put"
	attestPutCursor    = "ci-attest-put"
	attestPutBuffer    = 64
)

var _ = registerDaemonWiring(daemonRegistration{Name: attestPutSubsystem, Phase: phaseSupervised, Order: 24, Wire: wireCIAttestPut})

// unboundNodeChannel is the node channel until it is composed: it touches
// nothing and refuses every call.
func unboundNodeChannel(_ context.Context, _ string, _, _ any) error {
	return cascade.New(cascade.KindUnavailable, "node channel not composed (P1-NODE-18)")
}

func wireCIAttestPut(w *daemonWiring) error {
	if w.Deps.Fleet == nil || w.Bus == nil || w.Deps.Reloader == nil {
		w.Manifest.Skipped(attestPutSubsystem, "no fleet runtime, bus or config reloader")
		return nil
	}
	pub, err := newAttestPutPublisher(w)
	if err != nil {
		return err
	}
	if pending, err := pub.pending(w.Ctx); err != nil {
		return err
	} else if err := pub.recover(w.Ctx, pending); err != nil {
		pub.logger().Warn("ci attest put: recovery left a put unfinished", "error", err)
	}
	sub, err := w.Bus.Subscribe(w.Ctx, completionAcceptedTopic, attestPutCursor, attestPutBuffer)
	if err != nil {
		return err
	}
	w.Manifest.GoSupervised(w.Ctx, attestPutSubsystem, "accepted jobs to ci.attest.put", func(ctx context.Context) error {
		defer func() { _ = sub.Unsubscribe() }()
		return pub.run(ctx, sub)
	})
	return nil
}

// completionAcceptedTopic is the namespace of jobs.EventJobAccepted.
const completionAcceptedTopic = "jobs.accepted"

// attestPutRepos reads the node to repo ids table from the live [nodes]
// table through nodes.LoadSection. A table that is not a table was already
// refused when the config was admitted; here it reads as no targets.
func attestPutRepos(w *daemonWiring) func() (map[string][]string, error) {
	return func() (map[string][]string, error) {
		raw, _ := w.Deps.Reloader.Current().Extra["nodes"].(map[string]interface{})
		sec, err := nodes.LoadSection(raw)
		if err != nil {
			return nil, err
		}
		return sec.Repos, nil
	}
}

func newAttestPutPublisher(w *daemonWiring) (*attestPutPublisher, error) {
	db := w.Deps.Fleet.JobsDB()
	rows, err := jobs.NewCiAttestationStore(db)
	if err != nil {
		return nil, err
	}
	nonces, err := jobs.NewAttestationNonceStore(db, w.Clock)
	if err != nil {
		return nil, err
	}
	trust, err := attestTrustStore(w.Paths.DataDir(), w.Clock)
	if err != nil {
		return nil, err
	}
	effects, err := openEffectLog(w.Store, w.Clock, w.Bus, w.Paths)
	if err != nil {
		return nil, err
	}
	attention := daemon.NewAttentionStore(w.Store, w.Clock, w.Bus)
	pub := &attestPutPublisher{rows: rows, nonces: nonces, trust: trust, effects: effects, log: w.Logger,
		records: nodes.NewRecordStore(nodes.NewFileRecordBackend(w.Paths.DataDir()), w.Clock),
		repos:   attestPutRepos(w),
		dial:    func(string) nodeCall { return unboundNodeChannel },
		sign: func(ctx context.Context, msg []byte) ([]byte, error) {
			s, err := productionEnvelopeSigner(ctx, w.Paths)
			if err != nil {
				return nil, err
			}
			return s.Sign(ctx, msg)
		}}
	pub.attention = func(ctx context.Context, sourceRef string) {
		_, err := attention.Push(ctx, supervision.AttentionItem{Kind: supervision.KindError, SourceRef: sourceRef,
			ScopeRef: scope.Ref{Kind: scope.ScopeKindGlobal}})
		if err != nil {
			pub.logger().Error("ci attest put: attention item not filed", "error", err)
		}
	}
	pub.pending = effects.PendingEffects
	return pub, nil
}

// acceptedEvent is the jobs.accepted payload the publisher reads.
type acceptedEvent struct {
	Nonce string `json:"attestation_nonce"`
}

func (p *attestPutPublisher) run(ctx context.Context, sub *events.Subscription) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-sub.Errs:
			if ok && err != nil && ctx.Err() == nil {
				return err
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			if ev.Kind == jobs.EventJobAccepted {
				p.onAccepted(ctx, ev)
			}
		}
	}
}

func (p *attestPutPublisher) onAccepted(ctx context.Context, ev events.Event) {
	var a acceptedEvent
	if err := json.Unmarshal(ev.Payload, &a); err != nil {
		p.logger().Warn("ci attest put: undecodable jobs.accepted payload", "seq", ev.Seq, "error", err)
		return
	}
	if err := p.publish(ctx, a.Nonce); err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		p.logger().Warn("ci attest put: publish incomplete", "seq", ev.Seq, "error", err)
	}
}
