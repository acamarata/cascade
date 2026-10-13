// Purpose: the driver's job-id table: claiming the caller's
//   spec.JobID (R-14.329) or minting one, releasing a claim a later guard
//   refused, registering a started job, and looking one up.
// Inputs: the optional spec.JobID; a started jobRecord.
// Outputs: the claimed id; the registered record; ErrJobNotFound.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   from driver.go for the 300-line cap. A claim is atomic under d.mu, so
//   two concurrent Spawns cannot both take one id; an id already live or
//   claimed in this driver refuses with KindConflict and starts no child.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// claimID reserves want (when non-empty) or a freshly minted id.
func (d *Driver) claimID(want provider.AgentJobID) (provider.AgentJobID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if want != "" {
		if d.jobs[want] != nil || d.reserved[want] {
			return "", cascade.Newf(cascade.KindConflict, "claude: job id %q is already live in this driver", want)
		}
		d.reserved[want] = true
		return want, nil
	}
	for {
		d.nextID++
		id := provider.AgentJobID("claude-" + strconv.FormatUint(d.nextID, 10))
		if d.jobs[id] == nil && !d.reserved[id] {
			d.reserved[id] = true
			return id, nil
		}
	}
}

// unclaim releases a reservation whose Spawn refused before registering.
func (d *Driver) unclaim(id provider.AgentJobID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.reserved, id)
}

func (d *Driver) lookup(id provider.AgentJobID) (*jobRecord, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, ok := d.jobs[id]
	if !ok {
		return nil, provider.ErrJobNotFound
	}
	return rec, nil
}

// register moves a started job from the reservations to the live table.
func (d *Driver) register(rec *jobRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.reserved, rec.id)
	d.jobs[rec.id] = rec
}
