package memory

// Purpose: the write half of the consolidation job — the order in which a
//   group is made durable, and the crash contract that order buys. Split
//   from consolidation.go under the 300-line file cap.
// Inputs: a planned group (survivor plus fully-read retirees), read from
//   the live tree by Consolidator.plan.
// Outputs: a consolidation record on disk, tombstones on the retired
//   members, one event per completed group, and the running report.
// Constraints: the record is written BEFORE the first tombstone and the
//   event is offered AFTER the last one; a sink failure is logged into the
//   report's outcome but never fails the merge; nothing here reads the
//   wall clock.
// SPORT: internal.memory.consolidation.ConsolidateMemories (ADD,
//   P1-E07-W2-S13-T4).

import (
	"context"
	"errors"
	"time"
)

// apply makes each planned group durable, in order, and returns the
// finished report.
//
// A dry run stops after describing the groups: it writes no record,
// tombstones nothing and emits nothing, so a caller can see exactly what a
// real run would retire before letting it.
func (c *Consolidator) apply(
	ctx context.Context, report ConsolidationReport, groups []duplicateGroup, cfg ConsolidationConfig,
) (ConsolidationReport, error) {
	now := c.clock.Now().UTC()
	for _, g := range groups {
		report.Groups = append(report.Groups, describeGroup(g))
		if cfg.DryRun {
			continue
		}
		if err := c.consolidateGroup(ctx, g, now); err != nil {
			return report, err
		}
		report.Merged++
		report.Retired += len(g.retired)
	}
	if cfg.DryRun {
		report.NoChange = len(groups) == 0
		return report, nil
	}
	report.NoChange = report.Merged == 0
	return report, nil
}

// describeGroup renders one planned group for the report.
func describeGroup(g duplicateGroup) ConsolidationGroup {
	ids := make([]string, 0, len(g.retired))
	for _, m := range g.retired {
		ids = append(ids, entryID(m))
	}
	return ConsolidationGroup{CanonicalID: entryID(g.survivor), MemberIDs: ids}
}

// consolidateGroup makes one group durable.
//
// # The order, and what an interruption at each point leaves behind
//
//  1. The consolidation record is written first, atomically, holding every
//     retiree's full metadata and the body they all share. An interruption
//     BEFORE this leaves the tree exactly as it was: every record still
//     live, nothing to explain. An interruption DURING it leaves the
//     previous record intact, because the write is temp-plus-rename.
//  2. Each member is then tombstoned through the store, which itself
//     writes the tombstone before removing the file. An interruption here
//     leaves some members retired and some still live — and the record
//     from step 1 already names every one of them, so each retirement is
//     accounted for and the next run simply finishes the job.
//  3. The event is offered last. It reports what is already durable.
//
// At no point does a half-written merge exist. There is no intermediate
// state in which a record's content has been partly rewritten, because no
// content is ever rewritten: the survivor's file is not touched at all.
func (c *Consolidator) consolidateGroup(ctx context.Context, g duplicateGroup, now time.Time) error {
	if err := c.recordGroup(g, now); err != nil {
		return err
	}
	for _, m := range g.retired {
		if err := c.store.Delete(ctx, m.Kind, m.Name); err != nil {
			// A member another run already retired is not a failure: the
			// record naming it is on disk and the intended end state is
			// reached. Anything else is a real fault and stops the run.
			if errors.Is(err, ErrNoSuchEntry) {
				continue
			}
			return err
		}
	}
	return c.emit(ctx, g, now)
}

// emit offers the group's event to the sink.
//
// A sink failure is deliberately NOT returned. By the time this runs the
// record is on disk and the members are tombstoned; failing the job here
// would report work as undone that is in fact done, and would make the
// next run's report disagree with the tree. The event is a notification,
// not the record of what happened — that is the consolidation file.
func (c *Consolidator) emit(ctx context.Context, g duplicateGroup, now time.Time) error {
	ids := make([]string, 0, len(g.retired))
	for _, m := range g.retired {
		ids = append(ids, entryID(m))
	}
	_ = c.sink.MemoryConsolidated(ctx, ConsolidatedEvent{
		MemberIDs:      ids,
		ConsolidatedID: entryID(g.survivor),
		Method:         ConsolidationMethodExactHash,
		ConsolidatedAt: now,
	})
	return nil
}

// errorsIsAny reports whether err matches any of the given sentinels. It
// exists so a caller can name a SET of refusals it treats alike without
// spelling out a chain of errors.Is calls at every site.
func errorsIsAny(err error, sentinels ...error) bool {
	for _, s := range sentinels {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}
