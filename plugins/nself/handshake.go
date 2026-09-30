// Purpose (this file): the nself handshake handler — ONE function two
// entry points call: the nself_add_cascade tool (plugin.go, always
// PROPOSE mode, writes nothing) and the human-invoked `cascade nself
// handshake` CLI command (APPLY mode, calls the ConfigApplier). It reads
// the real verbs `nself version --json` and `nself config get <KEY>`
// (probe.go's activeRunner, reused directly — no new subprocess machinery)
// and proposes a server-profile diff a human or configApplier can apply.
// Inputs: a root directory (an nself project) and the handshake mode.
// Outputs: a handshakeResponse naming every proposed/applied/unchanged/
// skipped entry, the missing required server env-refs (names only, never
// values), and whether a daemon restart is required.
// Constraints: never reads or requests a secret-shaped config key
// (handshakeConfigKeys is fixed and tested); never echoes an env-ref's
// VALUE, only its name; PROPOSE mode never calls configApplier. A probed
// value that is credential-shaped (payload.go's scrub patterns, or refused
// by the bound LiteralScreener, the config writer's own validator) is
// withheld: never proposed, never applied, named only by its config path.
// Every response string passes payload.go's scrub before encoding; the
// code-chosen DSN-shape hint is attached after it (handshake_entries.go).
// SPORT: plugins/nself handshake (ADD) — P1-E25-W5-S103-T1.

package nself

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// handshakeOwner is the ConfigDiff.Owner / [plugins.<owner>] namespace.
const handshakeOwner = "cascade-nself"

// minNselfVersion is Art.2's recorded transcript floor.
const minNselfVersion = "1.3.5"

// handshakeConfigKeys are the ONLY keys ever read via `nself config get`.
// TestHandshakeNeverRequestsSecretKeys asserts none is secret-shaped.
var handshakeConfigKeys = []string{
	"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB", "POSTGRES_EXTENSIONS",
	"REDIS_ENABLED", "REDIS_PORT", "MINIO_ENABLED", "MINIO_PORT", "S3_BUCKET",
}

// requiredServerEnvRefs are the env-ref NAMES cmd/cascade/profile_server.go
// requires for --profile server. CASCADE_STORAGE_PGVECTOR_DSN is
// deliberately absent: it falls back to CASCADE_STORAGE_POSTGRES_DSN.
var requiredServerEnvRefs = []string{
	"CASCADE_STORAGE_POSTGRES_DSN", "CASCADE_STORAGE_REDIS_URL",
	"CASCADE_STORAGE_S3_ENDPOINT", "CASCADE_STORAGE_S3_BUCKET",
	"CASCADE_STORAGE_S3_KEY_ID", "CASCADE_STORAGE_S3_SECRET",
}

// handshakeGetenv resolves an env-ref's PRESENCE only — never its value.
// Injectable for hermetic tests.
var handshakeGetenv = os.Getenv

// handshakeProbeTimeout bounds every `nself version`/`nself config get`
// call, matching probe.go's own DETECTION-section bound.
const handshakeProbeTimeout = 2 * time.Second

// handshakeMode selects propose (never writes) or apply (writes through
// configApplier).
type handshakeMode string

const (
	handshakeModePropose handshakeMode = "propose"
	handshakeModeApply   handshakeMode = "apply"
)

// diffEntryWire is one proposed (unclassified) entry's wire shape.
type diffEntryWire struct {
	Path    string `json:"path"`
	Literal string `json:"literal"`
}

// outcomeWire is one classified entry's wire shape.
type outcomeWire struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// handshakeResponse is nself_add_cascade / `cascade nself handshake`'s
// wire shape.
type handshakeResponse struct {
	Status          string          `json:"status"`
	Proposed        []diffEntryWire `json:"proposed,omitempty"`
	Applied         []outcomeWire   `json:"applied,omitempty"`
	Unchanged       []outcomeWire   `json:"unchanged,omitempty"`
	Skipped         []outcomeWire   `json:"skipped,omitempty"`
	Withheld        []string        `json:"withheld,omitempty"`
	MissingEnv      []string        `json:"missing_env,omitempty"`
	DSNShape        string          `json:"dsn_shape,omitempty"`
	RestartRequired bool            `json:"restart_required,omitempty"`
	Note            string          `json:"note,omitempty"`
}

// nselfVersionInfo is `nself version --json`'s decoded shape.
type nselfVersionInfo struct {
	Version string `json:"version"`
}

// runHandshake is the one handler both entry points call. rootDir is made
// absolute first, so the recorded project_dir never depends on the
// working directory the command happened to run from.
func runHandshake(ctx context.Context, mode handshakeMode, rootDir string) (handshakeResponse, error) {
	dir, err := filepath.Abs(rootDir)
	if err != nil {
		return handshakeResponse{}, cascade.Wrap(cascade.KindInvalidInput, err, "cascade-nself: resolve the project directory")
	}
	screen := literalScreener
	if screen == nil {
		return handshakeResponse{}, cascade.New(cascade.KindUnavailable,
			"cascade-nself: no literal screener is bound; refusing to propose unscreened values")
	}
	if err := checkProject(ctx, dir); err != nil {
		return handshakeResponse{}, err
	}
	entries, withheld := withholdCredentialShaped(buildProposedEntries(dir, probeConfigValues(ctx, dir)), screen)
	missing := missingServerEnvRefs()
	if len(missing) == 0 {
		entries = append(entries, ConfigEntry{Path: "runtime.profile", Literal: `"server"`})
	}
	resp := proposeResponse(entries, missing)
	if mode == handshakeModeApply {
		if resp, err = applyHandshake(ctx, entries, missing); err != nil {
			return handshakeResponse{}, err
		}
	}
	resp.Withheld = withheld
	return finishResponse(resp, missing), nil
}

// checkProject refuses a directory that is not an nself project
// (KindNotFound) or whose nself is older than minNselfVersion
// (KindUnsupported).
func checkProject(ctx context.Context, dir string) error {
	if !newDetector(dir).Detect(ctx).Detected {
		return cascade.Newf(cascade.KindNotFound, "cascade-nself: %s is not an nself project", dir)
	}
	ver, err := probeVersion(ctx, dir)
	if err != nil {
		return err
	}
	if !versionAtLeast(ver.Version, minNselfVersion) {
		return cascade.Newf(cascade.KindUnsupported,
			"cascade-nself: nself %q is older than the minimum supported version %s", ver.Version, minNselfVersion)
	}
	return nil
}

// proposeResponse builds PROPOSE mode's response: it never calls
// configApplier (TestHandshakeToolPathWritesNothing).
func proposeResponse(entries []ConfigEntry, missing []string) handshakeResponse {
	status := "proposed"
	if len(missing) > 0 {
		status = "pending-env"
	}
	wire := make([]diffEntryWire, 0, len(entries))
	for _, e := range entries {
		wire = append(wire, diffEntryWire(e))
	}
	return handshakeResponse{Status: status, Proposed: wire, MissingEnv: missing}
}

// applyHandshake runs APPLY mode: the ONLY path that ever calls
// configApplier.ApplyDiff.
func applyHandshake(ctx context.Context, entries []ConfigEntry, missing []string) (handshakeResponse, error) {
	result, err := configApplier.ApplyDiff(ctx, handshakeOwner, entries)
	if err != nil {
		return handshakeResponse{}, err
	}
	resp := handshakeResponse{
		Applied:         wireOutcomes(result.Applied),
		Unchanged:       wireOutcomes(result.Unchanged),
		Skipped:         wireOutcomes(result.Skipped),
		MissingEnv:      missing,
		RestartRequired: appliedPath(result, "runtime.profile"),
		Note:            doctorNoteForHandshake(result),
	}
	switch {
	case len(missing) > 0:
		resp.Status = "pending-env"
	case len(result.Applied) > 0:
		resp.Status = "applied"
	default:
		resp.Status = "unchanged"
	}
	return resp, nil
}

func wireOutcomes(outcomes []ConfigOutcome) []outcomeWire {
	wire := make([]outcomeWire, 0, len(outcomes))
	for _, o := range outcomes {
		wire = append(wire, outcomeWire(o))
	}
	return wire
}

func appliedPath(result ConfigResult, path string) bool {
	for _, o := range result.Applied {
		if o.Path == path {
			return true
		}
	}
	return false
}

// probeVersion runs `nself version --json` in dir, bounded, through
// probe.go's activeRunner (no new subprocess machinery: this file reuses
// the existing closed-env runner directly).
func probeVersion(ctx context.Context, dir string) (nselfVersionInfo, error) {
	out, err := activeRunner.Run(ctx, dir, nselfBinary, []string{"version", "--json"}, handshakeProbeTimeout)
	if err != nil {
		return nselfVersionInfo{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade-nself: nself version --json")
	}
	return decodeVersionInfo(out)
}

// decodeVersionInfo decodes `nself version --json`'s bytes by the EXACT
// key "version". encoding/json binds struct fields case-insensitively, so
// a struct decode read {"VERSION":"9.9.9"} as 9.9.9 and cleared the
// version floor (the review's fuzz finding); a map lookup matches only
// the real key. Split out of probeVersion so FuzzNselfVersionJSON can
// fuzz it without a subprocess in the loop.
func decodeVersionInfo(data []byte) (nselfVersionInfo, error) {
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nselfVersionInfo{}, cascade.Wrap(cascade.KindInternal, err, "cascade-nself: decode nself version --json")
	}
	raw, ok := fields["version"]
	if !ok {
		return nselfVersionInfo{}, nil
	}
	version, ok := raw.(string)
	if !ok {
		return nselfVersionInfo{}, cascade.New(cascade.KindInternal, "cascade-nself: nself version --json: version is not a string")
	}
	return nselfVersionInfo{Version: version}, nil
}

// probeConfigValues runs `nself config get <KEY>` for every
// handshakeConfigKeys entry. A non-zero exit (key unset) is silently
// omitted — never --reveal, never a key outside this fixed list.
func probeConfigValues(ctx context.Context, dir string) map[string]string {
	values := map[string]string{}
	for _, key := range handshakeConfigKeys {
		out, err := activeRunner.Run(ctx, dir, nselfBinary, []string{"config", "get", key}, handshakeProbeTimeout)
		if err != nil {
			continue
		}
		values[key] = strings.TrimSpace(string(out))
	}
	return values
}
