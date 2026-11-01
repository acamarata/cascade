// Package canon is the closed set of claude CLI hook events and the
// meaning of exit code 2 for each one, taken from a capture of the
// installed CLI's bundled hook-event map (testdata/real-cc-hooks).
//
// It is a standard-library-only leaf. Its two sentinel errors are plain
// errors; callers outside this package wrap them into the cascade error
// kinds. The package writes no file, no hook body and no settings file.
package canon

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Event is a verbatim hook event name as the CLI's hook-event map keys it.
type Event string

// The 33 hook events of the capture, in the capture's source order.
const (
	PreToolUse          Event = "PreToolUse"
	PostToolUse         Event = "PostToolUse"
	PostToolUseFailure  Event = "PostToolUseFailure"
	PostToolBatch       Event = "PostToolBatch"
	PermissionDenied    Event = "PermissionDenied"
	Notification        Event = "Notification"
	UserPromptSubmit    Event = "UserPromptSubmit"
	UserPromptExpansion Event = "UserPromptExpansion"
	SessionStart        Event = "SessionStart"
	Stop                Event = "Stop"
	StopFailure         Event = "StopFailure"
	SubagentStart       Event = "SubagentStart"
	SubagentStop        Event = "SubagentStop"
	PreCompact          Event = "PreCompact"
	PostCompact         Event = "PostCompact"
	PreModelSwitch      Event = "PreModelSwitch"
	PostModelSwitch     Event = "PostModelSwitch"
	SessionEnd          Event = "SessionEnd"
	PermissionRequest   Event = "PermissionRequest"
	Setup               Event = "Setup"
	TeammateIdle        Event = "TeammateIdle"
	TaskCreated         Event = "TaskCreated"
	TaskCompleted       Event = "TaskCompleted"
	Elicitation         Event = "Elicitation"
	ElicitationResult   Event = "ElicitationResult"
	ConfigChange        Event = "ConfigChange"
	InstructionsLoaded  Event = "InstructionsLoaded"
	WorktreeCreate      Event = "WorktreeCreate"
	WorktreeRemove      Event = "WorktreeRemove"
	CwdChanged          Event = "CwdChanged"
	FileChanged         Event = "FileChanged"
	DirectoryAdded      Event = "DirectoryAdded"
	MessageDisplay      Event = "MessageDisplay"
)

// ExitTwo classifies what a hook's exit code 2 does for an event.
type ExitTwo int

const (
	// Blocks means exit 2 stops the action the event reports.
	Blocks ExitTwo = iota + 1
	// FeedbackOnly means exit 2 shows stderr but nothing is blocked.
	FeedbackOnly
	// NoExitTwo means the event documents no exit-2 behaviour.
	NoExitTwo
	// AnyNonzeroFails means every nonzero exit fails the action
	// (WorktreeCreate, from its "Other exit codes -" line).
	AnyNonzeroFails
)

// RefusalExitCode is the exit code a refusing hook command ends with.
const RefusalExitCode = 2

// ErrUnknownEvent reports a name that is not one of the 33 canon events.
var ErrUnknownEvent = errors.New("canon: unknown hook event")

// ErrEventCannotBlock reports a refusal asked of an event whose exit 2
// does not block.
var ErrEventCannotBlock = errors.New("canon: event cannot block on exit 2")

// eventClass is the closed event table: every canon event and the class
// of its exit code 2. TestCanonMatchesCapture checks each class against
// the class its verbatim capture line maps to.
var eventClass = map[Event]ExitTwo{
	UserPromptSubmit:    Blocks,
	UserPromptExpansion: Blocks,
	PreToolUse:          Blocks,
	PostToolBatch:       Blocks,
	SubagentStop:        Blocks,
	TaskCreated:         Blocks,
	TaskCompleted:       Blocks,
	Stop:                Blocks,
	TeammateIdle:        Blocks,
	ConfigChange:        Blocks,
	PreCompact:          Blocks,
	PreModelSwitch:      Blocks,
	Elicitation:         Blocks,
	ElicitationResult:   Blocks,
	SessionStart:        FeedbackOnly,
	Setup:               FeedbackOnly,
	SubagentStart:       FeedbackOnly,
	PostToolUse:         FeedbackOnly,
	PostToolUseFailure:  FeedbackOnly,
	WorktreeCreate:      AnyNonzeroFails,
	PermissionDenied:    NoExitTwo,
	Notification:        NoExitTwo,
	StopFailure:         NoExitTwo,
	PostCompact:         NoExitTwo,
	PostModelSwitch:     NoExitTwo,
	SessionEnd:          NoExitTwo,
	PermissionRequest:   NoExitTwo,
	InstructionsLoaded:  NoExitTwo,
	WorktreeRemove:      NoExitTwo,
	CwdChanged:          NoExitTwo,
	FileChanged:         NoExitTwo,
	DirectoryAdded:      NoExitTwo,
	MessageDisplay:      NoExitTwo,
}

// Parse returns the canon event named exactly s. Case variants and
// surrounding whitespace are refused with an error wrapping
// ErrUnknownEvent.
func Parse(s string) (Event, error) {
	ev := Event(s)
	if _, ok := eventClass[ev]; !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownEvent, s)
	}
	return ev, nil
}

// Names returns a sorted copy of the 33 canon event names.
func Names() []string {
	out := make([]string, 0, len(eventClass))
	for ev := range eventClass {
		out = append(out, string(ev))
	}
	sort.Strings(out)
	return out
}

// ExitTwo returns the class of e's exit code 2, or 0 for a name outside
// the canon.
func (e Event) ExitTwo() ExitTwo {
	return eventClass[e]
}

// RefusalCommand returns a POSIX sh command that prints reason to stderr
// and exits RefusalExitCode. Only an event whose class is Blocks can
// refuse; any other canon event yields ErrEventCannotBlock and a name
// outside the canon yields ErrUnknownEvent.
func RefusalCommand(ev Event, reason string) (string, error) {
	class, ok := eventClass[ev]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownEvent, string(ev))
	}
	if class != Blocks {
		return "", fmt.Errorf("%w: %s", ErrEventCannotBlock, string(ev))
	}
	quoted := "'" + strings.ReplaceAll(reason, "'", `'\''`) + "'"
	return fmt.Sprintf("printf '%%s\\n' %s >&2; exit %d", quoted, RefusalExitCode), nil
}
