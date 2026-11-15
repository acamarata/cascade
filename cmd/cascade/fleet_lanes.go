// Purpose: `cascade fleet lanes list|show|explain <id>` CLI commands.
// Dials fleet.lanes.* JSON-RPC methods through the Go client SDK and
// renders tables or the versioned envelope under --json.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FleetTopologyEnvelopeVersion is the version field in every topology envelope.
const FleetTopologyEnvelopeVersion = "1"

// LaneDTO is the public data transfer object for a fleet lane.
type LaneDTO struct {
	ID               string    `json:"id"`
	RuntimeProfile   string    `json:"runtime_profile"`
	QuotaDomain      string    `json:"quota_domain"`
	ModelID          string    `json:"model_id"`
	Effort           string    `json:"effort"`
	LaneClass        string    `json:"lane_class"`
	InteractionClass string    `json:"interaction_class"`
	Health           string    `json:"health"`
	HealthReason     string    `json:"health_reason"`
	BaseShadowPrice  float64   `json:"base_shadow_price"`
	Ceiling          string    `json:"ceiling"`
	CeilingReason    string    `json:"ceiling_reason"`
	Retired          bool      `json:"retired"`
	DiscoveredAt     time.Time `json:"discovered_at"`
}

// BucketDTO is the public data transfer object for a quota bucket.
type BucketDTO struct {
	Name              string    `json:"name"`
	Window            string    `json:"window"`
	Source            string    `json:"source"`
	Limit             int64     `json:"limit"`
	RemainingFraction float64   `json:"remaining_fraction"`
	Confidence        float64   `json:"confidence"`
	ResetAt           time.Time `json:"reset_at"`
	ObservedAt        time.Time `json:"observed_at"`
}

// DomainDTO is the public data transfer object for a quota domain.
type DomainDTO struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	BillingTier string      `json:"billing_tier"`
	Quarantined bool        `json:"quarantined"`
	Holders     int         `json:"holders"`
	Buckets     []BucketDTO `json:"buckets"`
}

// ProfileDTO is the public data transfer object for a runtime profile.
type ProfileDTO struct {
	ID         string `json:"id"`
	Runtime    string `json:"runtime"`
	Persistent bool   `json:"persistent"`
	Local      bool   `json:"local"`
}

// LaneExplainDTO is the full topology derivation for a single lane.
type LaneExplainDTO struct {
	Lane    LaneDTO    `json:"lane"`
	Profile ProfileDTO `json:"profile"`
	Domain  DomainDTO  `json:"domain"`
}

// topologyEnvelope is the versioned result shape of every fleet topology method.
type topologyEnvelope struct {
	Version string          `json:"version"`
	Data    json.RawMessage `json:"data"`
}

// newFleetLanesCmd builds `fleet lanes` with list, show and explain subcommands.
func newFleetLanesCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lanes",
		Short: "Inspect fleet lanes",
	}
	cmd.AddCommand(newFleetLanesListCmd(deps))
	cmd.AddCommand(newFleetLanesShowCmd(deps))
	cmd.AddCommand(newFleetLanesExplainCmd(deps))
	return cmd
}

// newFleetLanesListCmd builds `fleet lanes list`.
func newFleetLanesListCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all fleet lanes",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			lanes, err := fetchFleetLanes(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(fleetLanesList(lanes))
		},
	}
}

// newFleetLanesShowCmd builds `fleet lanes show <id>`.
func newFleetLanesShowCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a single fleet lane",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			lane, err := fetchFleetLaneShow(cmd.Context(), deps, args[0])
			if err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(fleetLaneShow(lane))
		},
	}
}

// newFleetLanesExplainCmd builds `fleet lanes explain <id>`.
func newFleetLanesExplainCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "explain <id>",
		Short: "Explain a fleet lane's topology derivation",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ex, err := fetchFleetLaneExplain(cmd.Context(), deps, args[0])
			if err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(fleetLaneExplain(ex))
		},
	}
}

// fetchFleetLanes dials fleet.lanes.list through the daemon socket.
func fetchFleetLanes(ctx context.Context, deps fleetSessionsDeps) ([]LaneDTO, error) {
	c, err := dialFleetJobsClient(ctx, deps)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.Do(ctx, "fleet.lanes.list", map[string]bool{"include_retired": false}, &raw); err != nil {
		return nil, err
	}
	return unwrapLanes(raw)
}

// fetchFleetLaneShow dials fleet.lanes.show through the daemon socket.
func fetchFleetLaneShow(ctx context.Context, deps fleetSessionsDeps, id string) (LaneDTO, error) {
	c, err := dialFleetJobsClient(ctx, deps)
	if err != nil {
		return LaneDTO{}, err
	}
	var raw json.RawMessage
	if err := c.Do(ctx, "fleet.lanes.show", map[string]string{"id": id}, &raw); err != nil {
		return LaneDTO{}, err
	}
	return unwrapLane(raw)
}

// fetchFleetLaneExplain dials fleet.lanes.explain through the daemon socket.
func fetchFleetLaneExplain(ctx context.Context, deps fleetSessionsDeps, id string) (LaneExplainDTO, error) {
	c, err := dialFleetJobsClient(ctx, deps)
	if err != nil {
		return LaneExplainDTO{}, err
	}
	var raw json.RawMessage
	if err := c.Do(ctx, "fleet.lanes.explain", map[string]string{"id": id}, &raw); err != nil {
		return LaneExplainDTO{}, err
	}
	return unwrapExplain(raw)
}

// unwrapLanes decodes the versioned envelope into a lane slice.
func unwrapLanes(raw json.RawMessage) ([]LaneDTO, error) {
	var env topologyEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, cascade.New(cascade.KindInternal, "fleet lanes: invalid response")
	}
	var lanes []LaneDTO
	if err := json.Unmarshal(env.Data, &lanes); err != nil {
		return nil, cascade.New(cascade.KindInternal, "fleet lanes: invalid data")
	}
	return lanes, nil
}

// unwrapLane decodes the versioned envelope into a single lane.
func unwrapLane(raw json.RawMessage) (LaneDTO, error) {
	var env topologyEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return LaneDTO{}, cascade.New(cascade.KindInternal, "fleet lanes: invalid response")
	}
	var lane LaneDTO
	if err := json.Unmarshal(env.Data, &lane); err != nil {
		return LaneDTO{}, cascade.New(cascade.KindInternal, "fleet lanes: invalid data")
	}
	return lane, nil
}

// unwrapExplain decodes the versioned envelope into a lane explanation.
func unwrapExplain(raw json.RawMessage) (LaneExplainDTO, error) {
	var env topologyEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return LaneExplainDTO{}, cascade.New(cascade.KindInternal, "fleet lanes: invalid response")
	}
	var ex LaneExplainDTO
	if err := json.Unmarshal(env.Data, &ex); err != nil {
		return LaneExplainDTO{}, cascade.New(cascade.KindInternal, "fleet lanes: invalid data")
	}
	return ex, nil
}

// fleetLanesList wraps []LaneDTO for output.Writer.
type fleetLanesList []LaneDTO

// String renders the human table for fleet lanes list.
func (l fleetLanesList) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "ID\tPROFILE\tHEALTH\tRETIRED\n")
	for _, lane := range l {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n",
			lane.ID, lane.RuntimeProfile, lane.Health, lane.Retired)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// MarshalJSON emits the versioned envelope for --json mode.
func (l fleetLanesList) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal([]LaneDTO(l))
	if err != nil {
		return nil, err
	}
	return json.Marshal(topologyEnvelope{Version: FleetTopologyEnvelopeVersion, Data: data})
}

// fleetLaneShow wraps LaneDTO for output.Writer.
type fleetLaneShow LaneDTO

// String renders the human table for fleet lanes show.
func (s fleetLaneShow) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "FIELD\tVALUE\n")
	_, _ = fmt.Fprintf(tw, "ID\t%s\n", s.ID)
	_, _ = fmt.Fprintf(tw, "PROFILE\t%s\n", s.RuntimeProfile)
	_, _ = fmt.Fprintf(tw, "HEALTH\t%s\n", s.Health)
	_, _ = fmt.Fprintf(tw, "CEILING\t%s\n", s.Ceiling)
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// MarshalJSON emits the versioned envelope for --json mode.
func (s fleetLaneShow) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(LaneDTO(s))
	if err != nil {
		return nil, err
	}
	return json.Marshal(topologyEnvelope{Version: FleetTopologyEnvelopeVersion, Data: data})
}

// fleetLaneExplain wraps LaneExplainDTO for output.Writer.
type fleetLaneExplain LaneExplainDTO

// String renders the human table for fleet lanes explain.
func (e fleetLaneExplain) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "SECTION\tFIELD\tVALUE\n")
	_, _ = fmt.Fprintf(tw, "LANE\tID\t%s\n", e.Lane.ID)
	_, _ = fmt.Fprintf(tw, "LANE\tHEALTH\t%s\n", e.Lane.Health)
	_, _ = fmt.Fprintf(tw, "LANE\tCEILING\t%s\n", e.Lane.Ceiling)
	_, _ = fmt.Fprintf(tw, "PROFILE\tID\t%s\n", e.Profile.ID)
	_, _ = fmt.Fprintf(tw, "DOMAIN\tID\t%s\n", e.Domain.ID)
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// MarshalJSON emits the versioned envelope for --json mode.
func (e fleetLaneExplain) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(LaneExplainDTO(e))
	if err != nil {
		return nil, err
	}
	return json.Marshal(topologyEnvelope{Version: FleetTopologyEnvelopeVersion, Data: data})
}
