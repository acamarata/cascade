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
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// socketPlaceholder is the literal token every HookDescriptor's
// CommandTemplate carries in place of the resolved daemon socket path.
const socketPlaceholder = "{{CASCADE_SOCKET_PATH}}"

// binaryPlaceholder is the token a CommandTemplate carries in place of the
// absolute path of the cascade binary. Render substitutes it, already
// single-quoted for POSIX sh, so a hook command never resolves cascade
// through PATH.
const binaryPlaceholder = "{{CASCADE_BIN_PATH}}"

// ErrEmptySocketPath is returned when Render is asked to render against
// an empty socket path — there is nothing to substitute, and rendering a
// command that still carries the literal placeholder would silently
// install a broken hook.
var ErrEmptySocketPath = cascade.New(cascade.KindInvalidInput, "hookpacks: socket path is empty")

// ErrBinaryPathNotAbsolute is returned when a pack that names the cascade
// binary is rendered against an empty or relative binary path.
var ErrBinaryPathNotAbsolute = cascade.New(cascade.KindInvalidInput, "hookpacks: cascade binary path is not absolute")

// hookCommandEntry is one entry in a harness hook loader's "hooks" array
// under a "type":"command" hook.
type hookCommandEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Timeout is the harness's own per-hook timeout field, in seconds,
	// omitted when the descriptor states none (P1-E16-W4-S34-T4). The
	// field name and unit are the harness's, confirmed against the real
	// client's embedded hook documentation — see
	// testdata/cc-hook-fixtures/README.md.
	Timeout int `json:"timeout,omitempty"`
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
//
// A pack whose templates name the cascade binary (binaryPlaceholder) is
// rendered against the cleaned running executable path, preserving symlinks. A pack that
// never names it does not look the executable up, so it cannot fail on it.
func (p HookPack) Render(socketPath string) ([]json.RawMessage, error) {
	if socketPath == "" {
		return nil, ErrEmptySocketPath
	}
	binaryPath := ""
	if p.namesBinary() {
		exe, err := resolveCascadeBinary()
		if err != nil {
			return nil, err
		}
		binaryPath = exe
	}
	return p.renderAgainst(socketPath, binaryPath)
}

// RenderWithBinary is Render with the cascade binary named by the caller
// instead of resolved from the running process: a harness installer that
// installs a binary other than itself, and the tests that run the rendered
// command against a binary built for them. binaryPath must be absolute; a
// relative or empty path is refused (ErrBinaryPathNotAbsolute) because it
// would put the hook back on PATH lookup.
func (p HookPack) RenderWithBinary(socketPath, binaryPath string) ([]json.RawMessage, error) {
	if socketPath == "" {
		return nil, ErrEmptySocketPath
	}
	return p.renderAgainst(socketPath, binaryPath)
}

// namesBinary reports whether any descriptor template carries binaryPlaceholder.
func (p HookPack) namesBinary() bool {
	for _, d := range p.Descriptors {
		if strings.Contains(d.CommandTemplate, binaryPlaceholder) {
			return true
		}
	}
	return false
}

// renderAgainst renders every descriptor with socketPath and binaryPath
// substituted in one pass, so no substituted value is ever re-scanned for a
// placeholder.
func (p HookPack) renderAgainst(socketPath, binaryPath string) ([]json.RawMessage, error) {
	if p.namesBinary() && !filepath.IsAbs(binaryPath) {
		return nil, ErrBinaryPathNotAbsolute
	}
	sub := strings.NewReplacer(
		"'"+socketPlaceholder+"'", shellSingleQuote(socketPath),
		binaryPlaceholder, shellSingleQuote(binaryPath),
	)
	out := make([]json.RawMessage, 0, len(p.Descriptors))
	for _, d := range p.Descriptors {
		if strings.Contains(strings.ReplaceAll(d.CommandTemplate, "'"+socketPlaceholder+"'", ""), socketPlaceholder) {
			return nil, cascade.New(cascade.KindInvalidInput, "hookpacks: socket placeholder must be single-quoted")
		}
		entry := hookConfigEntry{
			Matcher: d.Matcher,
			Hooks: []hookCommandEntry{{
				Type:    "command",
				Command: sub.Replace(d.CommandTemplate),
				Timeout: d.TimeoutSeconds,
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

// resolveCascadeBinary returns the cleaned absolute path of the running
// executable without resolving installation symlinks. Linux os.Executable
// already resolves /proc/self/exe; a removed binary still fails closed.
func resolveCascadeBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "hookpacks: cannot locate the cascade binary")
	}
	return filepath.Clean(exe), nil
}

// shellSingleQuote quotes s as one POSIX sh word: inside single quotes
// nothing is special, and an embedded single quote closes the quoting,
// adds an escaped quote and reopens it.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
