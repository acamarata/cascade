package nodes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the node's DURABLE record of which dispatched
//
//	actions it has started, which is what makes redelivery dedup real.
//
// Inputs: an action id, and the node's data directory.
// Outputs: whether this node has already started that action.
// Constraints: the reservation is written and FSYNCED before Reserve
//
//	returns, because the whole guarantee is about what survives a crash. A
//	log that buffered the write would still refuse duplicates for the life
//	of the process and forget them all on a power cut — which is precisely
//	the window a redelivery arrives in, so an unsynced log would be dedup
//	that works exactly when it is not needed.
//
//	One file per action rather than one appended index: a torn append can
//	lose or corrupt neighbouring records, while a rename into place is
//	atomic per action and a half-written temp file is simply not a
//	reservation.
//
// SPORT: internal/nodes:action-log (ADD) — P1-E17-W4-S37-T2.

// FileActionLog is a durable, file-backed ActionLog.
type FileActionLog struct {
	dir string
	mu  sync.Mutex
}

// NewFileActionLog opens the action log under dataDir.
func NewFileActionLog(dataDir string) *FileActionLog {
	return &FileActionLog{dir: filepath.Join(dataDir, "nodes", "actions")}
}

// actionState is what one reservation file holds.
type actionState struct {
	ActionID string          `json:"action_id"`
	Outcome  DispatchOutcome `json:"outcome,omitempty"`
}

// Reserve records actionID as started, reporting already=true when this
// node had already recorded it.
//
// The mutex serializes same-process callers; the O_EXCL create is what
// makes the check-and-record atomic against anything else, including a
// second node process sharing the directory.
func (l *FileActionLog) Reserve(_ context.Context, actionID string) (bool, error) {
	if actionID == "" {
		return false, errUnidentifiedAction()
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "nodes: opening the action log")
	}
	path := l.pathFor(actionID)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return true, nil
		}
		return false, cascade.Wrap(cascade.KindUnavailable, err, "nodes: reserving an action")
	}
	if err := writeAndSync(f, actionState{ActionID: actionID}); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return false, nil
}

// Complete records the terminal outcome for actionID.
//
// It never un-reserves: the reservation is the dedup fact and must outlive
// the outcome, or a redelivery after a completed action would run it again.
func (l *FileActionLog) Complete(_ context.Context, actionID string, outcome DispatchOutcome) error {
	if actionID == "" {
		return errUnidentifiedAction()
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	path := l.pathFor(actionID)
	if _, err := os.Stat(path); err != nil {
		return cascade.Newf(cascade.KindNotFound,
			"nodes: action %s was completed without ever being reserved", actionID)
	}
	f, err := os.CreateTemp(l.dir, ".complete-*")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: completing an action")
	}
	tmp := f.Name()
	if err := writeAndSync(f, actionState{ActionID: actionID, Outcome: outcome}); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: committing an action outcome")
	}
	return nil
}

// Outcome reports the recorded terminal outcome for actionID, if any.
func (l *FileActionLog) Outcome(actionID string) (DispatchOutcome, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	raw, err := os.ReadFile(l.pathFor(actionID))
	if err != nil {
		return "", false
	}
	var state actionState
	if err := json.Unmarshal(raw, &state); err != nil || state.Outcome == "" {
		return "", false
	}
	return state.Outcome, true
}

// pathFor renders one action's reservation path.
//
// The id is hex-encoded rather than used directly: an action id reaches
// this as a filename, and one carrying a separator would otherwise address
// a path outside the log.
func (l *FileActionLog) pathFor(actionID string) string {
	return filepath.Join(l.dir, encodeActionID(actionID)+".json")
}

// encodeActionID renders an action id safely as one path segment.
func encodeActionID(actionID string) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, len(actionID)*2)
	for i := 0; i < len(actionID); i++ {
		out = append(out, hexDigits[actionID[i]>>4], hexDigits[actionID[i]&0x0f])
	}
	return string(out)
}

// writeAndSync encodes state into f and fsyncs before returning.
func writeAndSync(f *os.File, state actionState) error {
	defer func() { _ = f.Close() }()
	encoded, err := json.Marshal(state)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "nodes: encoding an action reservation")
	}
	if _, err := f.Write(encoded); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: writing an action reservation")
	}
	if err := f.Sync(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: syncing an action reservation")
	}
	return nil
}
