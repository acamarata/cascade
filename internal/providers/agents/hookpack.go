// Purpose: RenderAgentHookPack writes the relay hook entries into the pack
//
//	directory the host shim hands the claude driver as HookPackPath. The
//	driver copies the pack into the leased tree's .claude/ and the harness
//	then runs "<exe> hook relay <event>" for PreToolUse, PostToolUse and
//	Stop.
//
// Inputs: the pack directory and the absolute path of the cascade binary.
// Outputs: dst/settings.json with the three relay entries added.
// Constraints: an existing settings.json is merged, never replaced: every
//
//	other top-level key and every existing hook entry survives with its
//	content and order, and the relay entry is appended once (running the
//	render twice changes nothing). The write is atomic (temp file, rename)
//	and 0600. A pack directory or settings file that is a symlink is refused.
//	exe must be absolute and free of characters a shell would interpret
//	(off Windows that includes any backslash; on Windows, one at the end).
//
// SPORT: internal.providers.agents/ADD (P1-AGT-14, contract:agent-hook-relay).

package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	packSettingsName = "settings.json"
	relayHookTimeout = 5
)

// relayExeShape is the only shape of binary path the pack will embed: no
// quote, dollar, backtick, glob, redirect or control character, so a
// double-quoted form is one safe word in sh and in cmd (the backslash is a
// Windows path separator).
var (
	relayExeShape = regexp.MustCompile(`^[A-Za-z0-9_./:\\ @+=,~-]+$`)
	relayExePlain = regexp.MustCompile(`^[A-Za-z0-9_./:-]+$`)
)

// relayHookEvents are the events the pack relays, in the order written.
var relayHookEvents = []string{HookPreToolUse, HookPostToolUse, HookStop}

// RenderAgentHookPack adds the relay entries to dst/settings.json. See the
// file comment for the contract.
func RenderAgentHookPack(dst string, exe string) error {
	if !filepath.IsAbs(exe) || !exeShellSafe(exe, runtime.GOOS) {
		return cascade.New(cascade.KindInvalidInput, "agents: hook pack: the relay binary path must be absolute and shell-safe")
	}
	if err := ensurePackDir(dst); err != nil {
		return err
	}
	path := filepath.Join(dst, packSettingsName)
	doc, err := readPackSettings(path)
	if err != nil {
		return err
	}
	hooks, err := decodeHooks(doc["hooks"])
	if err != nil {
		return err
	}
	for _, ev := range relayHookEvents {
		cmdLine := quoteExe(exe) + " hook relay " + ev
		if !hasHookCommand(hooks[ev], cmdLine) {
			hooks[ev] = append(hooks[ev], relayEntry(cmdLine))
		}
	}
	if doc["hooks"], err = json.Marshal(hooks); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "agents: hook pack: encoding the hooks")
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "agents: hook pack: encoding settings.json")
	}
	return writeFileAtomic(path, append(out, '\n'))
}

// exeShellSafe reports whether exe can be embedded as one double-quoted
// word on goos. Off Windows a backslash is refused outright: inside double
// quotes sh reads \" as an escaped quote, so a trailing backslash is a
// syntax error (exit 2, which blocks a PreToolUse call). On Windows the
// backslash is the path separator and is refused only directly before the
// closing quote.
func exeShellSafe(exe, goos string) bool {
	if !relayExeShape.MatchString(exe) {
		return false
	}
	if goos != "windows" {
		return !strings.Contains(exe, `\`)
	}
	return !strings.HasSuffix(exe, `\`)
}

// quoteExe returns exe as one shell word.
func quoteExe(exe string) string {
	if relayExePlain.MatchString(exe) {
		return exe
	}
	return `"` + exe + `"`
}

// relayEntry is one matcher group running cmdLine for every tool.
func relayEntry(cmdLine string) json.RawMessage {
	entry, _ := json.Marshal(map[string]any{
		"matcher": "",
		"hooks":   []map[string]any{{"type": "command", "command": cmdLine, "timeout": relayHookTimeout}},
	})
	return entry
}

// ensurePackDir creates dst (0700) when absent and refuses a symlink or a
// non-directory there.
func ensurePackDir(dst string) error {
	fi, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := os.MkdirAll(dst, 0o700); mkErr != nil {
			return cascade.Wrap(cascade.KindInternal, mkErr, "agents: hook pack: creating the pack directory")
		}
		return nil
	case err != nil:
		return cascade.Wrap(cascade.KindInternal, err, "agents: hook pack: examining the pack directory")
	case !fi.IsDir():
		return cascade.New(cascade.KindCapabilityDenied, "agents: hook pack: the pack path is not a real directory")
	}
	return nil
}

// readPackSettings returns the existing top-level keys of path (empty when
// the file is absent). A symlink, a non-regular file or invalid JSON refuses.
func readPackSettings(path string) (map[string]json.RawMessage, error) {
	doc := map[string]json.RawMessage{}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil || !fi.Mode().IsRegular() {
		return nil, cascade.New(cascade.KindCapabilityDenied, "agents: hook pack: settings.json is not a regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "agents: hook pack: reading settings.json")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "agents: hook pack: settings.json is not a JSON object")
	}
	return doc, nil
}

// decodeHooks parses the "hooks" value into event -> entries, keeping every
// entry as raw JSON so nothing in it is lost.
func decodeHooks(raw json.RawMessage) (map[string][]json.RawMessage, error) {
	hooks := map[string][]json.RawMessage{}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return hooks, nil
	}
	if err := json.Unmarshal(raw, &hooks); err != nil || hooks == nil {
		return nil, cascade.New(cascade.KindInvalidInput, `agents: hook pack: settings.json "hooks" is not an event map`)
	}
	return hooks, nil
}

// hasHookCommand reports whether any existing entry already runs cmdLine.
func hasHookCommand(entries []json.RawMessage, cmdLine string) bool {
	for _, e := range entries {
		var g struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if json.Unmarshal(e, &g) != nil {
			continue
		}
		for _, h := range g.Hooks {
			if h.Command == cmdLine {
				return true
			}
		}
	}
	return false
}

// writeFileAtomic writes body to path through a 0600 temp file in the same
// directory and a rename.
func writeFileAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "agents: hook pack: creating a temp file")
	}
	name := tmp.Name()
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, 0o600)); err != nil {
		_ = os.Remove(name)
		return cascade.Wrap(cascade.KindInternal, err, "agents: hook pack: writing settings.json")
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return cascade.Wrap(cascade.KindInternal, err, "agents: hook pack: replacing settings.json")
	}
	return nil
}
