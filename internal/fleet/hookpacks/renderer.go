package hookpacks

// Purpose (this file): substitute the resolved daemon socket path into a
//
//	HookPack's command templates and validate the result is parseable
//	hook configuration JSON.
//
// Inputs: a socket path resolved by the caller from [daemon].socket_path
//
//	(never hard-coded — this file has no default and no fallback path
//	literal anywhere in it).
//
// Outputs: one json.RawMessage per HookDescriptor, in
//
//	hookConfigEntry shape ({"matcher","hooks":[{"type","command"}]}),
//	ready to be grouped by event type into an installable hook
//	configuration file (registry.go's Render).
//
// Constraints: never stores socketPath in the template literal — every
//
//	call re-substitutes socketPlaceholder fresh. Render never runs a
//	shell or a network call itself; it only produces text, so it has no
//	daemon-absent failure mode of its own (the RENDERED command's own
//	non-blocking behavior is renderer_test.go's TestRenderedCommandFastWhenDaemonAbsent).
//
// CONTRACT DEVIATION (hook configuration fixture, recorded, not papered
// over). HOW step 2 asks Render's output to be "validated against a
// captured harness hook configuration fixture." files_scope.add lists three
// runtime HookPayload fixtures (testdata/cc-hook-fixtures/*.json) and no
// separate settings-file fixture, so there is no captured hook
// CONFIGURATION (settings.json) fixture in this ticket's scope to
// validate against. hookConfigEntry below is this package's own
// documented settings-entry shape (matcher + hooks[].type + .command),
// and renderer_test.go instead asserts Render's output decodes cleanly
// into that shape and round-trips — a structural parseability proof, not
// a captured-fixture comparison. Filed as an Art.9 gap in this ticket's
// journal.

import (
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// socketPlaceholder is the literal token every HookDescriptor's
// CommandTemplate carries in place of the resolved daemon socket path.
const socketPlaceholder = "{{CASCADE_SOCKET_PATH}}"

// ErrEmptySocketPath is returned when Render is asked to render against
// an empty socket path — there is nothing to substitute, and rendering a
// command that still carries the literal placeholder would silently
// install a broken hook.
var ErrEmptySocketPath = cascade.New(cascade.KindInvalidInput, "hookpacks: socket path is empty")

// hookCommandEntry is one entry in a harness hook loader's "hooks" array
// under a "type":"command" hook.
type hookCommandEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// hookConfigEntry is one rendered HookDescriptor's installable
// configuration entry: a matcher plus its command hooks.
type hookConfigEntry struct {
	Matcher string             `json:"matcher"`
	Hooks   []hookCommandEntry `json:"hooks"`
}

// Render substitutes socketPath into every descriptor in p and returns
// one json.RawMessage hookConfigEntry per descriptor, in the same order
// as p.Descriptors. socketPath must be non-empty (ErrEmptySocketPath
// otherwise) — Render never falls back to a guessed or default path.
func (p HookPack) Render(socketPath string) ([]json.RawMessage, error) {
	if socketPath == "" {
		return nil, ErrEmptySocketPath
	}
	out := make([]json.RawMessage, 0, len(p.Descriptors))
	for _, d := range p.Descriptors {
		entry := hookConfigEntry{
			Matcher: d.Matcher,
			Hooks: []hookCommandEntry{{
				Type:    "command",
				Command: strings.ReplaceAll(d.CommandTemplate, socketPlaceholder, socketPath),
			}},
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindInternal, err, "hookpacks: rendering %s descriptor", d.EventType)
		}
		out = append(out, raw)
	}
	return out, nil
}
