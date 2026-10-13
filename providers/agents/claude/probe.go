// Purpose: the pre-spawn harness probes (R-21.158, R-14.288): the
//   installed version from `--version`, and the stream-json system/init
//   event from Descriptor.ProbeArgv, checked against the revision-1 golden
//   before the job's first turn.
// Inputs: the binary, the job's exact driver environ and leased root.
// Outputs: the probed version; the peer ProtocolRange ({1,1} when the init
//   event passes the revision-1 shape, empty otherwise); typed refusals for
//   an MCP server that is not connected or a plugin that did not load.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   out for the 300-line cap. A failed, timed-out or unparsable probe
//   refuses; there is no configured or assumed fallback version. Every
//   probe child runs in its own process group and is killed and reaped.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	defaultProbeTimeout = 60 * time.Second
	maxProbeLine        = 1 << 20
	probePrompt         = "ping"
)

// ErrPluginLoad and ErrMCPServerUnavailable refuse a run whose init event
// reports a plugin or MCP server that did not load (R-14.288).
var (
	ErrPluginLoad           = cascade.New(cascade.KindUnavailable, "claude: harness init reports a plugin load error")
	ErrMCPServerUnavailable = cascade.New(cascade.KindUnavailable, "claude: harness init reports an MCP server that is not connected")
)

var versionLine = regexp.MustCompile(`^\s*(\d+\.\d+\.\d+)(\s|$)`)

func incompatible(format string, args ...any) error {
	return cascade.Wrapf(cascade.KindConflict, provider.ErrHarnessIncompatible, "claude: "+format, args...)
}

// runProbe starts bin in its own process group and hands each stdout line
// to onLine until it returns true, stdout ends, or timeout passes. The group
// is always killed and the leader reaped. It returns whether onLine stopped
// the probe and, otherwise, the leader's wait error.
func runProbe(ctx context.Context, bin string, args, env []string, dir string, timeout time.Duration,
	onLine func([]byte) bool,
) (stopped bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = dir, env
	setProcessGroup(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, cascade.Wrapf(cascade.KindInternal, err, "claude: probe pipe")
	}
	if err := cmd.Start(); err != nil {
		return false, incompatible("starting the harness probe: %v", err)
	}
	pgid, finished := cmd.Process.Pid, make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = signalGroup(pgid, procgroup.SigKill)
			_ = out.Close()
		case <-finished:
		}
	}()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64<<10), maxProbeLine)
	for !stopped && sc.Scan() {
		stopped = onLine(sc.Bytes())
	}
	if stopped {
		_ = signalGroup(pgid, procgroup.SigKill)
	}
	waitErr := cmd.Wait()
	close(finished)
	_ = signalGroup(pgid, procgroup.SigKill) // any member the leader left behind
	if ctx.Err() == context.DeadlineExceeded && !stopped {
		return false, incompatible("harness probe timed out after %s", timeout)
	}
	return stopped, waitErr
}

// probeVersion runs `<bin> --version` and returns its major.minor.patch.
// A non-zero exit, no output or an unparsable first line refuses.
func probeVersion(ctx context.Context, bin string, env []string, dir string, timeout time.Duration) (string, error) {
	var first string
	seen := false
	_, err := runProbe(ctx, bin, []string{"--version"}, env, dir, timeout, func(line []byte) bool {
		if !seen {
			first, seen = string(line), true
		}
		return false
	})
	if err != nil {
		return "", incompatible("harness --version failed: %v", err)
	}
	m := versionLine.FindStringSubmatch(first)
	if m == nil {
		return "", incompatible("harness --version printed no major.minor.patch")
	}
	return m[1], nil
}

// initEvent is the revision-1 system/init projection this driver reads.
type initEvent struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	SessionID      string          `json:"session_id"`
	Version        string          `json:"claude_code_version"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permissionMode"`
	Tools          []string        `json:"tools"`
	MCPServers     []mcpServer     `json:"mcp_servers"`
	Plugins        json.RawMessage `json:"plugins"`
	PluginErrors   json.RawMessage `json:"plugin_errors"`
	MCPErrors      json.RawMessage `json:"mcp_server_errors"`
}

type mcpServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// revision1InitKeys are the keys the revision-1 golden requires
// (testdata/claude_headless_init.json).
var revision1InitKeys = []string{
	"type", "subtype", "session_id", "claude_code_version", "model", "permissionMode",
	"tools", "mcp_servers", "plugins",
}

// probeInit runs the descriptor's probe argv and checks the first
// system/init event. It returns the peer range: {1,1} when the event
// matches revision 1 and the version matches the probed one, else empty.
func (d *Driver) probeInit(ctx context.Context, in argvInput, env []string) (provider.ProtocolRange, error) {
	var raw []byte
	args := append(append([]string{}, d.cfg.Descriptor.ProbeArgv...), probeFlags(in)...)
	args = append(args, "--", probePrompt)
	_, err := runProbe(ctx, d.cfg.Binary, args, env, in.root, d.cfg.ProbeTimeout, func(line []byte) bool {
		var head struct{ Type, Subtype string }
		if json.Unmarshal(line, &head) == nil && head.Type == "system" && head.Subtype == "init" {
			raw = append([]byte(nil), line...)
			return true
		}
		return false
	})
	if raw == nil {
		return provider.ProtocolRange{}, incompatible("harness probe emitted no system/init event (%v)", err)
	}
	var ev initEvent
	if !revision1Init(raw, &ev) || ev.Version != in.version {
		return provider.ProtocolRange{}, nil
	}
	if err := initRefusal(ev, in); err != nil {
		return provider.ProtocolRange{}, err
	}
	return provider.ProtocolRange{Min: protocolRevision1, Max: protocolRevision1}, nil
}

// revision1Init decodes raw into ev and reports whether every revision-1
// key is present with the golden's type.
func revision1Init(raw []byte, ev *initEvent) bool {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || json.Unmarshal(raw, ev) != nil {
		return false
	}
	for _, k := range revision1InitKeys {
		if v, ok := keys[k]; !ok || string(v) == "null" {
			return false
		}
	}
	_, okV := parseSemver(ev.Version)
	return okV && ev.SessionID != "" && ev.Model != "" && ev.PermissionMode != ""
}

// initRefusal refuses a non-connected MCP server, any plugin entry outside
// the golden {name,path[,source]} shape, any non-empty plugin_errors or
// mcp_server_errors key, a requested plugin directory that loaded nothing,
// and a permission mode that does not match the tool policy.
func initRefusal(ev initEvent, in argvInput) error {
	for _, s := range ev.MCPServers {
		if s.Status != "connected" {
			return cascade.Wrapf(cascade.KindUnavailable, ErrMCPServerUnavailable, "claude: MCP server status %q", s.Status)
		}
	}
	if nonEmptyJSON(ev.PluginErrors) || nonEmptyJSON(ev.MCPErrors) {
		return ErrPluginLoad
	}
	var plugins []map[string]json.RawMessage
	if json.Unmarshal(ev.Plugins, &plugins) != nil {
		return ErrPluginLoad
	}
	loaded := false
	for _, p := range plugins {
		_, path, ok := pluginEntry(p)
		if !ok {
			return ErrPluginLoad
		}
		loaded = loaded || (in.pluginDirEmitted() && within(in.def.PluginDir, path))
	}
	if in.pluginDirEmitted() && !loaded {
		return cascade.Wrapf(cascade.KindUnavailable, ErrPluginLoad, "claude: no plugin loaded from the plugin directory")
	}
	bypass := ev.PermissionMode == "bypassPermissions"
	if bypass != (in.policy == provider.ToolPolicyAll) {
		return incompatible("harness permission mode %q does not match tool policy %q", ev.PermissionMode, in.policy)
	}
	return nil
}

// pluginEntry reads one init plugin entry: string name (non-empty) and path,
// plus the optional string source key the harness added by 2.1.288
// (testdata/protocol/1/init_plugins_2_1_288.json). Any other key, or a
// non-string value, is a shape the driver does not know and reports !ok.
func pluginEntry(p map[string]json.RawMessage) (name, path string, ok bool) {
	for k := range p {
		if k != "name" && k != "path" && k != "source" {
			return "", "", false
		}
	}
	if !jsonString(p["name"], &name) || !jsonString(p["path"], &path) || name == "" {
		return "", "", false
	}
	if raw, has := p["source"]; has {
		var source string
		if !jsonString(raw, &source) {
			return "", "", false
		}
	}
	return name, path, true
}

// jsonString decodes raw into s only when it is a JSON string: json.Unmarshal
// leaves s alone for null, which would pass as an empty string.
func jsonString(raw json.RawMessage, s *string) bool {
	return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, s) == nil
}

// nonEmptyJSON reports whether raw holds anything but absent, null, [] or {}.
func nonEmptyJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "[]" && s != "{}"
}
