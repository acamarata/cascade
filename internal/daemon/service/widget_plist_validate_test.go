// Purpose: platform-neutral table tests for matchWidgetPlist - the status
//
//	guard that vouches only for bytes equal to what the installer renders.
//	Whitespace or comment-only differences also read as edited (fail-closed).
//
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cruntime "github.com/acamarata/cascade/internal/runtime"
)

// validatorFixture builds a config for an injected HOME and returns it with
// the bundle executable the installer would record.
func validatorFixture(t *testing.T) (WidgetConfig, string) {
	t.Helper()
	home := t.TempDir()
	paths, err := cruntime.NewPathProvider(func(string) string { return "" }, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(home, "bin", "cascade-real")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("x"), 0o755); err != nil { //nolint:gosec // test stand-in
		t.Fatal(err)
	}
	if cli, err = filepath.EvalSymlinks(cli); err != nil {
		t.Fatal(err)
	}
	cfg := WidgetConfig{HomeDir: home, SystemRoot: filepath.Join(home, "sys"), Paths: paths, Executable: cli}
	return cfg, widgetExecutablePath(filepath.Join(home, "Applications", WidgetBundleName))
}

// renderFor renders the installer's plist for cfg with CASCADE_HOME = home.
func renderFor(t *testing.T, cfg WidgetConfig, exe, home string) string {
	t.Helper()
	cfg.Getenv = func(k string) string {
		if k == "CASCADE_HOME" {
			return home
		}
		return ""
	}
	env, err := widgetEnvironment(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(renderWidgetPlist(exe, env, widgetLogPath(cfg)))
}

// widgetPlistCase is one edit of the installer's plist and its verdict.
type widgetPlistCase struct {
	name  string
	edit  func(string) string
	valid bool
	home  string // CASCADE_HOME the installer's environment sets ("" = unset)
	cur   string // CASCADE_HOME status sees; "" means the same as home
	same  bool   // the "installer output" rows: nothing may differ
}

// Edit builders over the rendered plist text.
func swapEdit(t *testing.T, old, repl string) func(string) string {
	return func(s string) string {
		if !strings.Contains(s, old) {
			t.Fatalf("fixture lacks %q", old)
		}
		return strings.Replace(s, old, repl, 1)
	}
}

func addTopEdit(extra string) func(string) string {
	return func(s string) string {
		return strings.Replace(s, "<dict>\n\t<key>Env", "<dict>\n"+extra+"\t<key>Env", 1)
	}
}

func addEnvEdit(t *testing.T, extra string) func(string) string {
	return swapEdit(t, "\t\t<key>CASCADE_CLI_PATH", "\t\t"+extra+"<key>CASCADE_CLI_PATH")
}

func envEntry(k, v string) string {
	return "<key>" + k + "</key>\n\t\t<string>" + v + "</string>\n\t\t"
}

func topEntry(k, v string) string { return "\t<key>" + k + "</key>\n\t<string>" + v + "</string>\n" }

// widgetPlistCases builds the table over the fixture's rendered plist.
func widgetPlistCases(t *testing.T, cfg WidgetConfig, exe string) []widgetPlistCase {
	return append(widgetOKAndHomeCases(t, cfg), widgetHostileCases(t, cfg, exe)...)
}

// widgetOKAndHomeCases are the installer-output rows and CASCADE_HOME drift.
func widgetOKAndHomeCases(t *testing.T, cfg WidgetConfig) []widgetPlistCase {
	bare := func(s string) string { return s }
	homeA := filepath.Dir(widgetLogPath(cfg))
	homeB := filepath.Join(cfg.HomeDir, "otherhome")
	swap := func(o, r string) func(string) string { return swapEdit(t, o, r) }
	return []widgetPlistCase{
		{"installer written", bare, true, "", "", true},
		{"with CASCADE_HOME", bare, true, homeA, "", true},
		{"relative CASCADE_HOME recorded", bare, true, "rel", "", true},
		{"trailing-slash CASCADE_HOME recorded", bare, true, homeA + "/", "", true},
		{"cli path other absolute", swap(cfg.Executable, filepath.Join(cfg.HomeDir, "bin", "evil")), false, "", "", false},
		{"home changed", bare, false, homeA, homeB, false},
		{"home removed", bare, false, homeA, "-", false},
		{"home added", bare, false, "", homeA, false},
	}
}

// widgetHostileCases are edits a hijacked agent would make.
func widgetHostileCases(t *testing.T, cfg WidgetConfig, exe string) []widgetPlistCase {
	status := widgetStatusFilePath(cfg)
	sock := cfg.Paths.SocketPath()
	logp := widgetLogPath(cfg)
	swap := func(o, r string) func(string) string { return swapEdit(t, o, r) }
	addTop, addEnv := addTopEdit, func(k, v string) func(string) string { return addEnvEdit(t, envEntry(k, v)) }
	addEnv2 := func(x string) func(string) string { return addEnvEdit(t, x) }
	str := topEntry
	return []widgetPlistCase{
		{"key-as-value Program", addTop("\t<key>Program</key><key>/tmp/evil</key>\n"), false, "", "", false},
		{"key-as-value env DYLD", addEnv2("<key>DYLD_INSERT_LIBRARIES</key><key>/tmp/evil.dylib</key>\n\t\t"), false, "", "", false},
		{"trailing duplicate CLI key", swap("\t</dict>\n\t<key>KeepAlive", "\t\t<key>CASCADE_CLI_PATH</key><key>/tmp/evil</key>\n\t</dict>\n\t<key>KeepAlive"), false, "", "", false},
		{"key inside ProgramArguments", swap("<string>"+exe+"</string>", "<string>"+exe+"</string>\n\t\t<key>--x</key>"), false, "", "", false},
		{"binary plist", func(string) string { return "bplist00\x00\x01" }, false, "", "", false},
		{"whitespace only change", func(s string) string { return s + "\n" }, false, "", "", false},
		{"key with trailing space", swap("<key>Label</key>", "<key>Label </key>"), false, "", "", false},
		{"added Program", addTop(str("Program", "/bin/sh")), false, "", "", false},
		{"added BundleProgram", addTop(str("BundleProgram", "Contents/MacOS/x")), false, "", "", false},
		{"added WorkingDirectory", addTop(str("WorkingDirectory", "/")), false, "", "", false},
		{"extra env DYLD_INSERT_LIBRARIES", addEnv("DYLD_INSERT_LIBRARIES", "/x.dylib"), false, "", "", false},
		{"status path rewritten", swap(status, filepath.Join(filepath.Dir(status), "forged.json")), false, "", "", false},
		{"socket path rewritten", swap(sock, sock+".evil"), false, "", "", false},
		{"relative cli path", swap(cfg.Executable, "cascade-real"), false, "", "", false},
		{"RunAtLoad false", swap("<key>RunAtLoad</key>\n\t<true/>", "<key>RunAtLoad</key>\n\t<false/>"), false, "", "", false},
		{"KeepAlive true", swap("<key>KeepAlive</key>\n\t<false/>", "<key>KeepAlive</key>\n\t<true/>"), false, "", "", false},
		{"StandardOutPath elsewhere", swap("<key>StandardOutPath</key>\n\t<string>"+logp, "<key>StandardOutPath</key>\n\t<string>"+logp+".x"), false, "", "", false},
		{"Label changed", swap("<string>"+WidgetLabel+"</string>", "<string>"+WidgetLabel+"x</string>"), false, "", "", false},
		{"extra argv", swap("<string>"+exe+"</string>", "<string>"+exe+"</string>\n\t\t<string>-x</string>"), false, "", "", false},
		{"relative CASCADE_HOME added", addEnv("CASCADE_HOME", "rel"), false, "", "", false},
		{"nested dict", addTop("\t<key>Sockets</key>\n\t<dict><key>a</key><dict/></dict>\n"), false, "", "", false},
	}
}

func TestWidgetPlistValidTable(t *testing.T) {
	cfg, exe := validatorFixture(t)
	for _, tc := range widgetPlistCases(t, cfg, exe) {
		t.Run(tc.name, func(t *testing.T) {
			base := renderFor(t, cfg, exe, tc.home)
			data := tc.edit(base)
			if !tc.same && data == base && tc.cur == "" {
				t.Fatal("edit did not change the plist")
			}
			seen := tc.home
			if tc.cur == "-" {
				seen = ""
			} else if tc.cur != "" {
				seen = tc.cur
			}
			cfg := cfg
			cfg.Getenv = func(k string) string {
				if k == "CASCADE_HOME" {
					return seen
				}
				return ""
			}
			bundle, sp, ok := matchWidgetPlist(cfg, []byte(data))
			if ok != tc.valid {
				t.Fatalf("matchWidgetPlist ok = %v, want %v", ok, tc.valid)
			}
			if ok && (bundle != filepath.Dir(filepath.Dir(filepath.Dir(exe))) || sp != widgetStatusFilePath(cfg)) {
				t.Fatalf("matched inputs bundle=%q status=%q", bundle, sp)
			}
		})
	}
}
