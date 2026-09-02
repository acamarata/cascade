package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: tests for logger.go — NewLogger's JSON/text output, level
//   filtering and SetLevel hot-reload, LogFilePath, and NewLogProvider's
//   error propagation from a directory it cannot create (Art.7.1: every
//   log file here lives under t.TempDir()).
// SPORT: runtime/logger (ADD, per T-2 sport_updates).

func TestNewLogger_EmitsValidJSONLines(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewLogger(&buf, loggingSection{Level: "info", Format: "json"})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Slog().Info("hello", "k", "v")
	l.Slog().Warn("world")

	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", line, err)
		}
		for _, field := range []string{"level", "time", "msg"} {
			if _, ok := decoded[field]; !ok {
				t.Errorf("line %q missing field %q", line, field)
			}
		}
	}
	if !strings.Contains(buf.String(), `"k":"v"`) {
		t.Errorf("structured field not emitted: %s", buf.String())
	}
}

func TestNewLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewLogger(&buf, loggingSection{Level: "info", Format: "text"})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Slog().Info("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Errorf("text output = %q, want msg=hello", buf.String())
	}
}

func TestNewLogger_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewLogger(&buf, loggingSection{Level: "info", Format: "json"})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Slog().Debug("suppressed")
	if buf.Len() != 0 {
		t.Fatalf("debug line at info level: %q", buf.String())
	}
	l.Slog().Info("emitted")
	if !strings.Contains(buf.String(), "emitted") {
		t.Errorf("info line missing: %q", buf.String())
	}
}

func TestLogger_SetLevel_UpdatesWithoutRestart(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewLogger(&buf, loggingSection{Level: "info", Format: "json"})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Slog().Debug("still suppressed")
	if buf.Len() != 0 {
		t.Fatalf("debug line before SetLevel: %q", buf.String())
	}

	l.SetLevel(slog.LevelDebug)
	l.Slog().Debug("now emitted")
	if !strings.Contains(buf.String(), "now emitted") {
		t.Errorf("debug line after SetLevel(Debug) missing: %q", buf.String())
	}
}

func TestNewLogger_UnrecognisedLevel(t *testing.T) {
	_, err := NewLogger(&bytes.Buffer{}, loggingSection{Level: "verbose"})
	var logErr *LogError
	if err == nil {
		t.Fatal("NewLogger: want *LogError for an unrecognised level, got nil")
	}
	if ok := asLogError(err, &logErr); !ok {
		t.Fatalf("NewLogger error = %v (%T), want *LogError", err, err)
	}
}

func asLogError(err error, target **LogError) bool {
	le, ok := err.(*LogError)
	if ok {
		*target = le
	}
	return ok
}

func TestLogFilePath(t *testing.T) {
	root := t.TempDir()
	p, err := NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return root
		}
		return ""
	}, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	want := filepath.Join(root, "logs", "cascade.log")
	if got := LogFilePath(p); got != want {
		t.Errorf("LogFilePath = %q, want %q", got, want)
	}
}

func TestNewLogProvider_LogDirectoryNotWritable(t *testing.T) {
	dir := t.TempDir()
	// "logs" exists as a plain FILE, so PathProvider.LogDir()'s directory
	// creation (os.MkdirAll inside NewRotatingWriter) fails regardless of
	// platform or process privilege — a portable way to force the
	// "log directory not writable" error path (unlike chmod 0o000, which
	// root/Windows can bypass).
	if err := writeFile(t, filepath.Join(dir, "logs"), "not a directory"); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}
	p, err := NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return dir
		}
		return ""
	}, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}

	_, err = NewLogProvider(loggingSection{Level: "info", Format: "json"}, p, NewFixedClock(fixedTime))
	var logErr *LogError
	if !asLogError(err, &logErr) {
		t.Fatalf("NewLogProvider error = %v (%T), want *LogError", err, err)
	}
}

func TestLogError_Error(t *testing.T) {
	withField := &LogError{Field: "logging.level", Reason: "must be one of debug, info, warn, error"}
	if !strings.Contains(withField.Error(), "logging.level") {
		t.Errorf("Error() = %q, want it to name the field", withField.Error())
	}
	withoutField := &LogError{Reason: "boom"}
	if !strings.Contains(withoutField.Error(), "boom") {
		t.Errorf("Error() = %q, want the reason", withoutField.Error())
	}
}

func TestParseLogLevel_AllValues(t *testing.T) {
	cases := map[string]slog.Level{
		"":      slog.LevelInfo,
		"info":  slog.LevelInfo,
		"debug": slog.LevelDebug,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for in, want := range cases {
		got, err := parseLogLevel(in)
		if err != nil {
			t.Errorf("parseLogLevel(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestNewLogProvider_FullLifecycle exercises LogProvider's full method
// surface (Logger, SetLevel, Reconfigure, Close) plus Runtime.Close via
// Bootstrap, which wires a LogProvider from Config.Logging + PathProvider
// (T-2 contract task 3).
func TestNewLogProvider_FullLifecycle(t *testing.T) {
	root := t.TempDir()
	p, err := NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return root
		}
		return ""
	}, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}

	lp, err := NewLogProvider(loggingSection{Level: "info", Format: "json"}, p, NewFixedClock(fixedTime))
	if err != nil {
		t.Fatalf("NewLogProvider: %v", err)
	}
	if lp.Logger() == nil {
		t.Fatal("Logger() returned nil")
	}
	lp.Logger().Info("hello")
	lp.SetLevel(slog.LevelDebug)
	lp.Reconfigure(1, 2)
	if err := lp.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	content, err := os.ReadFile(LogFilePath(p))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "hello") {
		t.Errorf("log file missing emitted line: %q", string(content))
	}
}

// TestBootstrap_WiresLogProviderAndClose covers bootstrap.go's Log wiring
// (task 3) and Runtime.Close, including the nil-receiver/nil-Log
// no-op cases.
func TestBootstrap_WiresLogProviderAndClose(t *testing.T) {
	home := t.TempDir()
	rt, err := Bootstrap(context.Background(), BootstrapOptions{
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return home, nil },
		Environ: fakeEnviron(nil),
		Clock:   NewFixedClock(fixedTime),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if rt.Log == nil {
		t.Fatal("Runtime.Log is nil")
	}
	rt.Log.Logger().Info("bootstrap wired the logger")
	if err := rt.Close(); err != nil {
		t.Errorf("Runtime.Close: %v", err)
	}

	content, err := os.ReadFile(LogFilePath(rt.Paths))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "bootstrap wired the logger") {
		t.Errorf("log file missing emitted line: %q", string(content))
	}

	var nilRuntime *Runtime
	if err := nilRuntime.Close(); err != nil {
		t.Errorf("nil *Runtime Close() = %v, want nil", err)
	}
	if err := (&Runtime{}).Close(); err != nil {
		t.Errorf("Runtime{} (nil Log) Close() = %v, want nil", err)
	}
}
