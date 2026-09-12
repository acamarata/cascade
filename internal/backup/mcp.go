// Purpose: shared backup snapshot listing plus the read-only MCP
// registrations (cascade_backup_list, and S-42.T4's cascade_backup_verify).
// Inputs: named repository targets, outcome storage, and list/verify-only
// MCP calls.
// Outputs: the CLI/MCP snapshot or verification-report schema, or a typed
// refusal.
// Constraints: only read verbs are registered here; mutation and elevated
// verbs never enter the MCP registry (07 rationale 7).
// SPORT: internal.backup.mcp/ADD (P1-E19-W4-S42-T3); CHANGED (P1-E19-W4-S42-T4).

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// SnapshotSummary is the common `backup list --json` and MCP result row.
type SnapshotSummary struct {
	ID          SnapshotID `json:"id"`
	Created     time.Time  `json:"created"`
	Domains     []string   `json:"domains"`
	Target      string     `json:"target"`
	ObjectCount int        `json:"object_count"`
	Outcome     string     `json:"outcome"`
}

// NamedTarget binds a persisted target name to its repository driver.
type NamedTarget struct {
	Name   string
	Target Target
}

// SnapshotListFunc is the injected read operation used by the MCP tool.
type SnapshotListFunc func(context.Context) ([]SnapshotSummary, error)

// ListSnapshots enumerates and decrypts manifest metadata across targets.
func ListSnapshots(ctx context.Context, store provider.Store, namespace string, targets []NamedTarget) ([]SnapshotSummary, error) {
	if len(targets) == 0 {
		return []SnapshotSummary{}, nil
	}
	identity, err := AgeIdentity()
	if err != nil {
		return nil, err
	}
	var out []SnapshotSummary
	for _, target := range targets {
		rows, lerr := listTargetSnapshots(ctx, store, namespace, identity, target)
		if lerr != nil {
			return nil, lerr
		}
		out = append(out, rows...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func listTargetSnapshots(ctx context.Context, store provider.Store, namespace, identity string, target NamedTarget) ([]SnapshotSummary, error) {
	keys, err := target.Target.List(ctx, repoManifestsDir+"/")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "backup: list manifests for target %q", target.Name)
	}
	outcomes, err := ListOutcomes(ctx, store, namespace, target.Name)
	if err != nil {
		return nil, err
	}
	bySnapshot := outcomeIndex(outcomes)
	rows := make([]SnapshotSummary, 0, len(keys))
	for _, key := range keys {
		id, ok := snapshotIDFromManifestKey(key)
		if !ok {
			continue
		}
		manifest, merr := fetchManifestUnverified(ctx, target.Target, identity, id)
		if merr != nil {
			return nil, merr
		}
		rows = append(rows, summaryFromManifest(target.Name, manifest, bySnapshot[id]))
	}
	return rows, nil
}

func snapshotIDFromManifestKey(key string) (SnapshotID, bool) {
	const prefix, suffix = repoManifestsDir + "/", ".json"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
	return SnapshotID(id), id != ""
}

// outcomeIndex folds outcomes (chronological, oldest first -- ListOutcomes'
// own order) into one status per snapshot id, the LAST (most recent)
// outcome for that id winning. S-42.T4: a successful verification fire
// reports "verified" (the §22 VERIFIED state), distinct from "success" (a
// create fire only) -- `backup list`/`cascade_backup_list` can therefore
// show a snapshot as created-but-not-yet-verified vs actually verified,
// rather than conflating the two.
func outcomeIndex(outcomes []Outcome) map[SnapshotID]string {
	out := make(map[SnapshotID]string, len(outcomes))
	for _, outcome := range outcomes {
		if outcome.Snapshot == "" {
			continue
		}
		out[SnapshotID(outcome.Snapshot)] = outcomeStatus(outcome)
	}
	return out
}

func outcomeStatus(outcome Outcome) string {
	if !outcome.Success {
		return "failed"
	}
	if outcome.OutcomeEffectiveKind() == OutcomeKindVerify {
		return "verified"
	}
	return "success"
}

func summaryFromManifest(target string, manifest Manifest, outcome string) SnapshotSummary {
	if outcome == "" {
		outcome = "unknown"
	}
	return SnapshotSummary{
		ID: manifest.Snapshot, Created: manifest.Created, Domains: manifest.Domains,
		Target: target, ObjectCount: manifest.ObjectCount, Outcome: outcome,
	}
}

// MCPRegistration returns the sole backup MCP surface.
func MCPRegistration(list SnapshotListFunc) mcp.CoreRegistration {
	return mcp.CoreRegistration{
		Tool: mcp.Tool{
			Name: "cascade_backup_list", Description: "List backup snapshots and manifest metadata",
			PluginID: "cascade.core.backup",
		},
		Grants: []string{"read"},
		Handler: func(ctx context.Context, input []byte) ([]byte, error) {
			if err := validateListInput(input); err != nil {
				return nil, err
			}
			rows, err := list(ctx)
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(map[string]any{"snapshots": rows})
			if err != nil {
				return nil, cascade.Wrap(cascade.KindInternal, err, "backup: encode MCP list result")
			}
			return data, nil
		},
	}
}

func validateListInput(input []byte) error {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	return cascade.New(cascade.KindInvalidInput, "backup: cascade_backup_list takes no arguments")
}

// VerifyRunFunc is the injected read (well, read-plus-bookkeeping: it
// still records an Outcome and may push an attention item, per this
// ticket's own contract -- never a target mutation) operation the
// cascade_backup_verify MCP tool calls: target is the optional target
// name from the request ("" selects the single configured target, mirroring
// cmd/cascade's selectBackupRecord).
type VerifyRunFunc func(ctx context.Context, target string) (VerificationReport, error)

// verifyMCPInput is cascade_backup_verify's request shape: an optional
// target name, matching `backup verify --target` exactly (07 rationale 7 /
// S-42.T3's registration pattern: the MCP schema mirrors the CLI's).
type verifyMCPInput struct {
	Target string `json:"target,omitempty"`
}

// VerifyMCPRegistration returns the cascade_backup_verify MCP read
// surface (S-42.T4). Its result schema is VerificationReport encoded
// exactly as `backup verify --json` emits it in its envelope's data
// field -- the same struct, the same field names, deliberate schema
// parity with the CLI (this ticket's own acceptance criterion).
func VerifyMCPRegistration(run VerifyRunFunc) mcp.CoreRegistration {
	return mcp.CoreRegistration{
		Tool: mcp.Tool{
			Name: "cascade_backup_verify", Description: "Verify a backup target's most recent snapshot integrity",
			PluginID: "cascade.core.backup",
		},
		Grants: []string{"read"},
		Handler: func(ctx context.Context, input []byte) ([]byte, error) {
			target, err := parseVerifyInput(input)
			if err != nil {
				return nil, err
			}
			report, err := run(ctx, target)
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(report)
			if err != nil {
				return nil, cascade.Wrap(cascade.KindInternal, err, "backup: encode MCP verify result")
			}
			return data, nil
		},
	}
}

func parseVerifyInput(input []byte) (string, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var req verifyMCPInput
	if err := dec.Decode(&req); err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "backup: cascade_backup_verify request is malformed")
	}
	return req.Target, nil
}
