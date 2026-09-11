// Purpose: DeviceRecord (the machine row every later Q consumer reads) and
//
//	the RecordStore that persists it.
//
// Inputs: Identity + Tier at enrollment time; liveness/capability field
//
//	updates from S-36.T2's heartbeat (not this ticket).
//
// Outputs: a persisted DeviceRecord keyed by node id, or a typed
//
//	fail-closed error.
//
// Constraints: the contract asks for persistence "via the B/S-02 storage
//
//	abstraction," but internal/storage/domains.go's closed eleven-domain
//	set (R-14.5, amended R-16.51) has no `nodes` domain — its DomainSessions
//	entry names "nodes, lanes, journal" as owned by P1-E13-W3-S27-T1
//	(internal/fleet), a DIFFERENT ticket, and B/S-02 exposes no
//	generic domain-keyed Get/Set surface a package outside internal/storage
//	can call (the same gap internal/elevation's trust.go documented and
//	worked around). Rather than import internal/storage's sqlite driver
//	internals from outside its package boundary, or claim a domain no
//	ruling grants this ticket, RecordStore follows the identical
//	documented precedent: one JSON file under internal/runtime's
//	DataDir(), through an injectable Backend seam so tests never touch a
//	real CASCADE_HOME (Art.7.1). See the ticket journal's CONTRADICTIONS
//	section for the full quote.
//
// SPORT: internal/nodes DeviceRecord/ADDED, RecordStore/ADDED
//
//	(P1-E17-W4-S36-T1).

package nodes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DeviceRecord is one machine row: the enrolled identity, its trust_tier,
// and the liveness/capability fields S-36.T2's heartbeat keeps current.
// This ticket writes and reads the record; it does not implement the
// heartbeat update path itself.
type DeviceRecord struct {
	NodeID     string    `json:"node_id"`
	PubKeyB64  string    `json:"pubkey_b64"`
	Tier       Tier      `json:"trust_tier"`
	EnrolledAt time.Time `json:"enrolled_at"`
	// LastSeen is the most recent heartbeat timestamp. Zero until S-36.T2
	// writes the first heartbeat.
	LastSeen time.Time `json:"last_seen,omitempty"`
	// RevokedKeys holds the fingerprints of every superseded or revoked
	// public key this node id has ever carried (rotate.go). A frame
	// signed by any key whose fingerprint appears here is refused.
	RevokedKeys []string `json:"revoked_keys,omitempty"`
	// SyncCursors is opaque per-domain sync-cursor state (S-38.T2's,
	// preserved verbatim across rotation per R-21.220 — this ticket never
	// reads or writes individual cursor values, only round-trips the map).
	SyncCursors map[string]string `json:"sync_cursors,omitempty"`
}

// Clock abstracts time.Now so this package never reads the wall clock
// directly (forbidigo). Duck-typed against internal/runtime.Clock and
// internal/testkit.Clock, both of which already satisfy it, mirroring
// internal/elevation's identical Clock seam.
type Clock interface {
	Now() time.Time
}

// RecordBackend persists the full set of device records, keyed by node
// id. fileBackend is the production implementation; tests use an in-memory
// fake or a t.TempDir()-rooted fileBackend, never a real CASCADE_HOME.
type RecordBackend interface {
	// Load returns every persisted record, keyed by node id. An empty,
	// non-existent store returns an empty map and a nil error, never an
	// error — "no records yet" is a valid state, not a failure.
	Load() (map[string]DeviceRecord, error)
	// Save persists the full record set, replacing whatever was there.
	Save(map[string]DeviceRecord) error
}

// RecordStore is the device-record store: Enroll admits a new node,
// Get/List read back, Rotate/Revoke (rotate.go) mutate a record's key
// material in place while preserving its tier and sync cursors.
type RecordStore struct {
	backend RecordBackend
	clock   Clock
}

// NewRecordStore constructs a store over backend, driven by clock.
func NewRecordStore(backend RecordBackend, clock Clock) *RecordStore {
	return &RecordStore{backend: backend, clock: clock}
}

// ErrAlreadyEnrolled reports that a live identity is already enrolled
// under this node id. KindConflict: re-enroll of a live identity is a
// typed conflict, never a silent overwrite (this ticket's contract, both
// in the enroll task text and the acceptance criteria).
func ErrAlreadyEnrolled(nodeID string) error {
	return cascade.Newf(cascade.KindConflict,
		"nodes: node %q is already enrolled; re-enrollment of a live identity is refused (use rotate, not enroll)", nodeID)
}

// Enroll admits identity at the given tier as a new device record.
// Returns ErrAlreadyEnrolled if nodeID already has a record — re-enroll of
// a live identity is always a typed conflict, never a silent overwrite,
// regardless of whether the new key matches the old one.
func (s *RecordStore) Enroll(identity Identity, tier Tier) (DeviceRecord, error) {
	if _, ok := Rank(tier); !ok {
		return DeviceRecord{}, cascade.Newf(cascade.KindInvalidInput, "nodes: cannot enroll with invalid trust_tier %q", tier)
	}
	records, err := s.backend.Load()
	if err != nil {
		return DeviceRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "nodes: read device records")
	}
	if _, exists := records[identity.NodeID]; exists {
		return DeviceRecord{}, ErrAlreadyEnrolled(identity.NodeID)
	}
	rec := DeviceRecord{
		NodeID:     identity.NodeID,
		PubKeyB64:  identity.PubKeyB64(),
		Tier:       tier,
		EnrolledAt: s.clock.Now(),
	}
	records[identity.NodeID] = rec
	if err := s.backend.Save(records); err != nil {
		return DeviceRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "nodes: write device records")
	}
	return rec, nil
}

// Get returns the record for nodeID, or cascade.ErrNotFound.
func (s *RecordStore) Get(nodeID string) (DeviceRecord, error) {
	records, err := s.backend.Load()
	if err != nil {
		return DeviceRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "nodes: read device records")
	}
	rec, ok := records[nodeID]
	if !ok {
		return DeviceRecord{}, cascade.Newf(cascade.KindNotFound, "nodes: no device record for node %q", nodeID)
	}
	return rec, nil
}

// List returns every device record, sorted by node id for deterministic
// output.
func (s *RecordStore) List() ([]DeviceRecord, error) {
	records, err := s.backend.Load()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: read device records")
	}
	out := make([]DeviceRecord, 0, len(records))
	for _, r := range records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out, nil
}

// put writes rec back into the store, for use by rotate.go's Rotate/Revoke
// which mutate an existing record's key material and revoked-key set.
func (s *RecordStore) put(rec DeviceRecord) error {
	records, err := s.backend.Load()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: read device records")
	}
	records[rec.NodeID] = rec
	if err := s.backend.Save(records); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: write device records")
	}
	return nil
}

// fileRecordBackend is the production RecordBackend: one JSON file under
// dataDir/nodes/devices.json (mirrors internal/elevation's fileBackend
// precedent).
type fileRecordBackend struct {
	path string
}

// NewFileRecordBackend returns a RecordBackend that persists at
// <dataDir>/nodes/devices.json.
func NewFileRecordBackend(dataDir string) RecordBackend {
	return fileRecordBackend{path: filepath.Join(dataDir, "nodes", "devices.json")}
}

func (b fileRecordBackend) Load() (map[string]DeviceRecord, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]DeviceRecord{}, nil
		}
		return nil, err
	}
	var records map[string]DeviceRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "nodes: device record store is not valid JSON")
	}
	if records == nil {
		records = map[string]DeviceRecord{}
	}
	return records, nil
}

func (b fileRecordBackend) Save(records map[string]DeviceRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return runtime.WriteBytesAtomic(b.path, data)
}
