// Package pbd (lifecycle.go): the `pbd claim`/`pbd step`/`pbd done`
// commands (P1-E14-W3-S30-T1) and their plugin.pbd.* JSON-RPC mirrors,
// over internal/pews's claim/step/CR/QA/done state machine. `step` also
// carries CR and QA via its event field — 07's ratified pbd namespace
// mounts exactly claim/step/done, no separate cr/qa verb.
//
// Inputs: RunCommand's args (root, ticket id, operation id, phase) for
// the CLI path; a lifecycleRequest JSON object for the RPC path. Outputs:
// the appended pews.JournalEntry, or a *cascade.Error.
//
// Constraints: imports pkg/** and internal/pews ONLY (Art.10.2); no MCP
// tool is added (lifecycle writes are not MCP-safe). FileJournalStore is
// the shipped pews.JournalStore, instantiated at the plugin layer since a
// live Epic M internal/fleet/journal.SQLiteStore cannot be reached from
// plugins/** (see internal/pews/lifecycle.go's doc comment). Its Clock is
// optional: nil records TSUnixNano 0, a documented degrade rather than a
// stub, mirroring projector.go's nil-EventPublisher precedent.
//
// SPORT: plugins/pbd lifecycle (ADD) — P1-E14-W3-S30-T1.
package pbd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

const (
	claimCommandName = "claim"
	stepCommandName  = "step"
	doneCommandName  = "done"
	claimRPCMethod   = "plugin." + pluginID + "." + claimCommandName
	stepRPCMethod    = "plugin." + pluginID + "." + stepCommandName
	doneRPCMethod    = "plugin." + pluginID + "." + doneCommandName
)

// lifecycleJournalFile is the sidecar filename, sibling to tombstones.yaml.
const lifecycleJournalFile = "lifecycle-journal.yaml"

// lifecycleJournalDoc is the sidecar's on-disk shape, keyed by ticket id.
type lifecycleJournalDoc struct {
	Entities map[string][]lifecycleJournalEntry `yaml:"entities"`
}

type lifecycleJournalEntry struct {
	Seq         uint64 `yaml:"seq"`
	Event       string `yaml:"event"`
	OperationID string `yaml:"operation_id"`
	Payload     string `yaml:"payload,omitempty"`
	TSUnixNano  int64  `yaml:"ts_unix_nano"`
}

// FileJournalStore is the shipped pews.JournalStore: an append-only YAML sidecar, one entry per transition, guarded by an in-process mutex.
type FileJournalStore struct {
	root  string
	clock pews.Clock
	mu    sync.Mutex
}

// NewFileJournalStore constructs a FileJournalStore; clock may be nil.
func NewFileJournalStore(root string, clock pews.Clock) *FileJournalStore {
	return &FileJournalStore{root: root, clock: clock}
}

var _ pews.JournalStore = (*FileJournalStore)(nil)

// Append implements pews.JournalStore.
func (s *FileJournalStore) Append(_ context.Context, entityID string, event pews.LifecycleEvent, operationID string, payload json.RawMessage) (pews.JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return pews.JournalEntry{}, err
	}
	if doc.Entities == nil {
		doc.Entities = map[string][]lifecycleJournalEntry{}
	}
	var ts int64
	if s.clock != nil {
		ts = s.clock.Now().UnixNano()
	}
	seq := uint64(len(doc.Entities[entityID])) + 1
	e := lifecycleJournalEntry{Seq: seq, Event: string(event), OperationID: operationID, Payload: string(payload), TSUnixNano: ts}
	doc.Entities[entityID] = append(doc.Entities[entityID], e)
	if err := s.save(doc); err != nil {
		return pews.JournalEntry{}, err
	}
	return pews.JournalEntry{EntityID: entityID, Seq: seq, Event: event, OperationID: operationID, Payload: payload, TSUnixNano: ts}, nil
}

// Replay implements pews.JournalStore.
func (s *FileJournalStore) Replay(_ context.Context, entityID string) ([]pews.JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	raw := doc.Entities[entityID]
	out := make([]pews.JournalEntry, 0, len(raw))
	for _, e := range raw {
		out = append(out, pews.JournalEntry{EntityID: entityID, Seq: e.Seq, Event: pews.LifecycleEvent(e.Event),
			OperationID: e.OperationID, Payload: json.RawMessage(e.Payload), TSUnixNano: e.TSUnixNano})
	}
	return out, nil
}

// load reads the sidecar; a missing file is a fresh, empty journal.
func (s *FileJournalStore) load() (lifecycleJournalDoc, error) {
	path := filepath.Join(s.root, lifecycleJournalFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return lifecycleJournalDoc{}, nil
		}
		return lifecycleJournalDoc{}, cascade.Wrapf(cascade.KindInternal, err, "pbd: reading %q", path)
	}
	var doc lifecycleJournalDoc
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return lifecycleJournalDoc{}, cascade.Wrapf(cascade.KindInvalidInput, uerr, "pbd: malformed lifecycle journal %q", path)
	}
	return doc, nil
}

// save writes doc atomically (temp file, then rename).
func (s *FileJournalStore) save(doc lifecycleJournalDoc) error {
	data, merr := yaml.Marshal(doc)
	if merr != nil {
		return cascade.Wrap(cascade.KindInternal, merr, "pbd: encoding lifecycle journal YAML")
	}
	if merr := os.MkdirAll(s.root, 0o755); merr != nil {
		return cascade.Wrapf(cascade.KindInternal, merr, "pbd: creating directory %q", s.root)
	}
	tmp, terr := os.CreateTemp(s.root, ".lifecycle-journal-*.yaml.tmp")
	if terr != nil {
		return cascade.Wrapf(cascade.KindInternal, terr, "pbd: creating temp file in %q", s.root)
	}
	tmpPath, path := tmp.Name(), filepath.Join(s.root, lifecycleJournalFile)
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		return cleanupTemp(tmpPath, werr, "writing")
	}
	if werr := tmp.Sync(); werr != nil {
		_ = tmp.Close()
		return cleanupTemp(tmpPath, werr, "syncing")
	}
	if cerr := tmp.Close(); cerr != nil {
		return cleanupTemp(tmpPath, cerr, "closing")
	}
	if rerr := os.Rename(tmpPath, path); rerr != nil {
		return cleanupTemp(tmpPath, rerr, "renaming")
	}
	return nil
}

// cleanupTemp removes tmpPath and wraps err with what for the caller.
func cleanupTemp(tmpPath string, err error, what string) error {
	_ = os.Remove(tmpPath)
	return cascade.Wrapf(cascade.KindInternal, err, "pbd: %s %q", what, tmpPath)
}

// lifecycleRequest is the shared RPC/CLI request shape. Event applies
// only to step: "" or "step" (default), "cr", or "qa".
type lifecycleRequest struct {
	Root        string `json:"root"`
	Phase       string `json:"phase"`
	Ticket      string `json:"ticket"`
	OperationID string `json:"operation_id"`
	Event       string `json:"event,omitempty"`
	CRLevel     string `json:"cr_level,omitempty"`
	QALevel     string `json:"qa_level,omitempty"`
	Note        string `json:"note,omitempty"`
}

// decodeLifecycleRequest refuses an empty body or a missing field.
func decodeLifecycleRequest(data []byte) (lifecycleRequest, error) {
	if len(data) == 0 {
		return lifecycleRequest{}, cascade.New(cascade.KindInvalidInput, "pbd: lifecycle: a request body is required")
	}
	var req lifecycleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return lifecycleRequest{}, cascade.Wrap(cascade.KindInvalidInput, err, "pbd: lifecycle: malformed request")
	}
	if req.Root == "" || req.Ticket == "" || req.OperationID == "" {
		return lifecycleRequest{}, cascade.New(cascade.KindInvalidInput, "pbd: lifecycle: root, ticket, and operation_id are required")
	}
	return req, nil
}

// runLifecycle loads req's tree and dispatches to the named pews
// function, over a FileJournalStore rooted at req.Root.
func runLifecycle(ctx context.Context, verb string, req lifecycleRequest) (pews.JournalEntry, error) {
	phase := resolvePhase(req.Phase)
	tree, err := pews.NewStore(req.Root, phase).Load()
	if err != nil {
		return pews.JournalEntry{}, err
	}
	js := NewFileJournalStore(req.Root, nil)
	switch verb {
	case claimCommandName:
		return pews.Claim(ctx, tree, js, req.Ticket, req.OperationID)
	case doneCommandName:
		return pews.Done(ctx, tree, js, req.Ticket, req.OperationID)
	case stepCommandName:
		return runStepEvent(ctx, tree, js, req)
	default:
		return pews.JournalEntry{}, cascade.Newf(cascade.KindUnsupported, "pbd: no lifecycle verb named %q", verb)
	}
}

// runStepEvent dispatches step's event sub-field to Step/RecordCR/RecordQA.
func runStepEvent(ctx context.Context, tree *pews.Tree, js pews.JournalStore, req lifecycleRequest) (pews.JournalEntry, error) {
	eventName := req.Event
	if eventName == "" {
		eventName = "step"
	}
	event, perr := pews.ParseLifecycleEvent(eventName)
	if perr != nil {
		return pews.JournalEntry{}, cascade.Wrapf(cascade.KindInvalidInput, perr, "pbd: step: want \"\", \"step\", \"cr\", or \"qa\"")
	}
	switch event {
	case pews.EventStep:
		return pews.Step(ctx, tree, js, req.Ticket, req.OperationID, req.Note)
	case pews.EventCR:
		return pews.RecordCR(ctx, tree, js, req.Ticket, req.OperationID, pews.CRLevel(req.CRLevel))
	case pews.EventQA:
		return pews.RecordQA(ctx, tree, js, req.Ticket, req.OperationID, pews.QALevel(req.QALevel))
	case pews.EventClaim, pews.EventDone:
	}
	return pews.JournalEntry{}, cascade.Newf(cascade.KindInvalidInput, "pbd: step: event %q is not step/cr/qa", event)
}

// ClaimRPC is plugin.pbd.claim's handler body, shaped like
// internal/rpc.HandlerFunc by structural convention (status.go's
// StatusRPC doc comment explains why no import is needed here).
func ClaimRPC(ctx context.Context, params json.RawMessage) (any, error) {
	return lifecycleRPC(ctx, claimCommandName, params)
}

// StepRPC is plugin.pbd.step's handler body; see ClaimRPC.
func StepRPC(ctx context.Context, params json.RawMessage) (any, error) {
	return lifecycleRPC(ctx, stepCommandName, params)
}

// DoneRPC is plugin.pbd.done's handler body; see ClaimRPC.
func DoneRPC(ctx context.Context, params json.RawMessage) (any, error) {
	return lifecycleRPC(ctx, doneCommandName, params)
}

// lifecycleRPC is ClaimRPC/StepRPC/DoneRPC's shared body.
func lifecycleRPC(ctx context.Context, verb string, params json.RawMessage) (any, error) {
	req, err := decodeLifecycleRequest(params)
	if err != nil {
		return nil, err
	}
	return runLifecycle(ctx, verb, req)
}

// runLifecycleCommand is RunCommand's claim/step/done entry point. args:
// [0] root (required), [1] ticket id (required), [2] operation id
// (required), [3] phase (optional), and for step only [4] event
// ("", "step", "cr", "qa"), [5] cr_level, [6] qa_level, [7] note.
func runLifecycleCommand(ctx context.Context, name string, args []string) error {
	if len(args) < 3 || args[0] == "" || args[1] == "" || args[2] == "" {
		return cascade.Newf(cascade.KindInvalidInput, "pbd %s: root, ticket id, and operation id arguments are required", name)
	}
	req := lifecycleRequest{Root: args[0], Ticket: args[1], OperationID: args[2]}
	if len(args) > 3 {
		req.Phase = args[3]
	}
	if name == stepCommandName {
		req = withStepArgs(req, args)
	}
	_, err := runLifecycle(ctx, name, req)
	return err
}

// withStepArgs fills req's step-only fields from args[4:].
func withStepArgs(req lifecycleRequest, args []string) lifecycleRequest {
	if len(args) > 4 {
		req.Event = args[4]
	}
	if len(args) > 5 {
		req.CRLevel = args[5]
	}
	if len(args) > 6 {
		req.QALevel = args[6]
	}
	if len(args) > 7 {
		req.Note = args[7]
	}
	return req
}
