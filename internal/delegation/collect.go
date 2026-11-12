// Purpose: supervised collection, addressed results and ordered resource release.
// Deadlines use the injected clock; the supervisor joins both workers.

package delegation

import (
	"cmp"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

type collector struct {
	service       *Service
	notify        *notify.Service
	record        jobs.DelegationRecord
	env           envelope.Envelope
	handle        agents.SpawnHandle
	lease         capacity.Lease
	worktree      *Worktree
	start         time.Time
	maxWallClock  time.Duration
	ticks         <-chan struct{}
	releaseOnce   sync.Once
	releaseErr    error
	resourcesOnce sync.Once
	resourcesErr  error
}

type collectorSupervisor interface {
	GoSupervised(context.Context, string, string, func(context.Context) error)
}

func (c *collector) startSupervised(ctx context.Context, manifest collectorSupervisor) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ticks, pulses := c.ticks, make(chan struct{}, 1)
	c.ticks = pulses
	manifest.GoSupervised(ctx, "delegation.deadline."+c.record.ID, "enforcing delegation deadline", func(ctx context.Context) error {
		return c.watchDeadline(ctx, ticks, pulses, done, cancel)
	})
	manifest.GoSupervised(ctx, "delegation.collect."+c.record.ID, "collecting delegation", func(context.Context) error {
		defer cancel()
		defer close(done)
		return c.run(runCtx)
	})
}

func (c *collector) watchDeadline(ctx context.Context, ticks <-chan struct{}, pulses chan<- struct{}, done <-chan struct{}, cancel context.CancelFunc) error {
	deadline, _ := c.deadline()
	for {
		select {
		case <-ctx.Done():
			cancel()
			return c.releaseHandle(context.WithoutCancel(ctx))
		case <-done:
			return nil
		case _, ok := <-ticks:
			if !ok || !c.service.deps.Clock.Now().Before(deadline) {
				cancel()
				return c.releaseHandle(context.WithoutCancel(ctx))
			}
			select {
			case pulses <- struct{}{}:
			default:
			}
		}
	}
}

func (c *collector) deadline() (time.Time, string) {
	d := c.start.Add(min(c.env.Budget.WallClock, c.maxWallClock))
	if !c.env.ExpiresAt.After(d) {
		return c.env.ExpiresAt, "envelope expired"
	}
	return d, "wall clock"
}

func (c *collector) run(ctx context.Context) (err error) {
	cleanup := context.WithoutCancel(ctx)
	defer func() {
		if v := recover(); v != nil {
			_ = c.service.deps.Store.FinishDelegation(cleanup, c.record.ID, jobs.DelegationFailed, "collector panic")
			_ = c.release(cleanup)
			panic(v)
		}
		err = cmp.Or(err, c.release(cleanup))
	}()
	state, reason, err := c.collect(ctx)
	if deadline, why := c.deadline(); !c.service.deps.Clock.Now().Before(deadline) {
		state, reason = jobs.DelegationFailed, why
		err = c.releaseHandle(cleanup)
	} else if ctx.Err() != nil {
		state, reason, err = jobs.DelegationFailed, "daemon stopped", nil
	}
	if err != nil {
		state = jobs.DelegationFailed
		if reason == "" {
			reason = "collector failed"
		}
	}
	finishErr := c.service.deps.Store.FinishDelegation(cleanup, c.record.ID, state, reason)
	return cmp.Or(err, finishErr)
}

func (c *collector) collect(ctx context.Context) (jobs.DelegationState, string, error) {
	deadline, reason := c.deadline()
	for {
		if !c.service.deps.Clock.Now().Before(deadline) {
			return jobs.DelegationFailed, reason, c.releaseHandle(context.WithoutCancel(ctx))
		}
		if ctx.Err() != nil {
			return jobs.DelegationFailed, "daemon stopped", nil
		}
		state, err := c.handle.Driver.Status(ctx, c.handle.JobID)
		if err != nil {
			return jobs.DelegationFailed, "", err
		}
		if !state.Valid() {
			return jobs.DelegationFailed, "", cascade.New(cascade.KindInternal, "delegation: invalid driver state")
		}
		if state.Terminal() {
			return c.deliver(ctx, state)
		}
		select {
		case <-ctx.Done():
		case _, ok := <-c.ticks:
			if !ok {
				return jobs.DelegationFailed, "", cascade.New(cascade.KindUnavailable, "delegation: collector clock stopped")
			}
		}
	}
}

func (c *collector) deliver(ctx context.Context, state provider.AgentRunState) (jobs.DelegationState, string, error) {
	result, err := c.handle.Driver.Collect(ctx, c.handle.JobID)
	if err != nil {
		return jobs.DelegationFailed, "", err
	}
	if deadline, reason := c.deadline(); !c.service.deps.Clock.Now().Before(deadline) {
		return jobs.DelegationFailed, reason, c.releaseHandle(context.WithoutCancel(ctx))
	}
	terminal := jobs.DelegationFailed
	if state == provider.AgentRunAccepted {
		terminal = jobs.DelegationSucceeded
	}
	if state == provider.AgentRunCancelled {
		terminal = jobs.DelegationCancelled
	}
	result.DataClass = provider.JoinSensitivity(result.DataClass, c.env.Sensitivity)
	body, err := json.Marshal(waitResult{JobID: c.record.ID, State: terminal,
		Output: result.Output, Artifacts: result.Artifacts, DataClass: result.DataClass})
	if err == nil {
		err = c.notify.Notifier.Deliver(ctx, notify.Notification{ID: "delegation.result:" + c.record.ID,
			Source: "delegation.result", Class: notify.ClassAddressed, Visibility: notify.VisibilityPrivate,
			TargetPrincipal: c.record.OriginPrincipal, CorrelationID: c.record.ID, Kind: "delegation.result",
			Payload: body, Origin: notify.Origin{Source: "delegation.result", RecordRef: c.record.ID,
				Tier: result.DataClass, Trust: provider.ProvenanceUntrustedSource}})
	}
	return terminal, "", err
}

func (c *collector) releaseHandle(ctx context.Context) error {
	c.releaseOnce.Do(func() { c.releaseErr = c.handle.Release(ctx) })
	return c.releaseErr
}

func (c *collector) release(ctx context.Context) error {
	c.resourcesOnce.Do(func() {
		c.resourcesErr = c.releaseHandle(ctx)
		c.resourcesErr = cmp.Or(c.resourcesErr, c.worktree.Release())
		c.resourcesErr = cmp.Or(c.resourcesErr, c.service.deps.fleet.Release(ctx, c.lease))
		c.service.deps.Minter.Revoke(c.record.AgentPrincipal)
	})
	return c.resourcesErr
}

type waitResult struct {
	JobID     string                   `json:"job_id"`
	State     jobs.DelegationState     `json:"state"`
	Output    string                   `json:"output,omitempty"`
	Artifacts []string                 `json:"artifacts,omitempty"`
	DataClass provider.SensitivityTier `json:"data_class"`
}

// wait reads only the stored origin's addressed inbox, including after finish
// but before the notification dispatcher has drained the result.
func (s *Service) wait(ctx context.Context, notifications *notify.Service, id string, timeout time.Duration, ticks <-chan struct{}) (waitResult, error) {
	rec, err := s.deps.Store.GetDelegation(ctx, id)
	if err != nil {
		return waitResult{}, err
	}
	caller, _ := principal.CallerFrom(ctx)
	addressee, err := notify.AddresseeFrom(caller)
	if err != nil || (rec.OriginPrincipal == notify.OwnerCLITarget && !addressee.OwnerCLI) ||
		(rec.OriginPrincipal != notify.OwnerCLITarget && (addressee.OwnerCLI || addressee.PrincipalID != rec.OriginPrincipal)) {
		return waitResult{}, deniedf("only the delegation origin may wait")
	}
	end := s.deps.Clock.Now().Add(timeout)
	for {
		items, err := notifications.ReadAddressed(ctx, id)
		if err != nil {
			return waitResult{}, err
		}
		if result, found, err := addressedResult(items, id); found || err != nil {
			return result, err
		}
		if !s.deps.Clock.Now().Before(end) {
			rec, err = s.deps.Store.GetDelegation(ctx, id)
			return waitResult{JobID: id, State: rec.State, DataClass: provider.SensitivityLocalOnly}, err
		}
		select {
		case <-ctx.Done():
			return waitResult{}, cascade.Wrap(cascade.KindCanceled, ctx.Err(), "delegation: wait canceled")
		case _, ok := <-ticks:
			if !ok {
				return waitResult{}, cascade.New(cascade.KindUnavailable, "delegation: wait clock stopped")
			}
		}
	}
}

func addressedResult(items []notify.Notification, id string) (waitResult, bool, error) {
	for _, item := range items {
		if item.Kind != "delegation.result" {
			continue
		}
		var result waitResult
		if err := json.Unmarshal(item.Payload, &result); err != nil || result.JobID != id {
			return waitResult{}, false, cascade.New(cascade.KindIntegrity, "delegation: invalid addressed result")
		}
		result.DataClass = provider.JoinSensitivity(result.DataClass, item.DataClass)
		return result, true, nil
	}
	return waitResult{}, false, nil
}
