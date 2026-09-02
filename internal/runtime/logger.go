package runtime

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
)

// Purpose: the slog JSON/text handler wrapper (T-2 contract task 1) and
//   LogProvider, the constructor-injected pairing of that handler with a
//   RotatingWriter (rotation.go), built once by Bootstrap from *Config
//   and PathProvider (task 3).
// Inputs: NewLogger takes an io.Writer and the [logging] section
//   (config.go); NewLogProvider takes the section, a PathProvider, and an
//   injected Clock.
// Outputs: a *slog.Logger every later subsystem receives via constructor
//   injection — never through slog.SetDefault — plus SetLevel/
//   Reconfigure hooks C/S-05.T8's hot-reload engine calls on a config
//   reload, per 08 §3's "[logging] is fully hot" reload class.
// Constraints: no global slog.SetDefault mutation anywhere in this
//   package (T-2 contract task 1). PathProvider (paths.go) has no
//   dedicated LogPath() method — it is out of this ticket's files_scope —
//   so LogFilePath derives the single log file path from LogDir() here,
//   the one place that needs it.
// SPORT: runtime/logger (ADD, per T-2 sport_updates).

// logFileName is the single log file every daemon subsystem writes to,
// under PathProvider.LogDir().
const logFileName = "cascade.log"

// LogFilePath returns the resolved log file path under paths.LogDir().
func LogFilePath(paths PathProvider) string {
	return filepath.Join(paths.LogDir(), logFileName)
}

// LogError is the typed error the logging subsystem returns for an
// unopenable log file, an unrecognised level string, or a failed
// rotation step.
type LogError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *LogError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("runtime: log: %s: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("runtime: log: %s", e.Reason)
}

// Logger wraps a *slog.Logger with a live-updatable minimum level
// (SetLevel) so a hot-reload never needs to reconstruct the handler.
type Logger struct {
	levelVar *slog.LevelVar
	slog     *slog.Logger
}

// NewLogger builds a Logger writing to w at cfg's level and format.
// cfg.Format == "text" selects slog.TextHandler; anything else
// (including "" and the validated "json") selects slog.JSONHandler,
// matching parseLoggingSection's "json" default. An unrecognised
// cfg.Level is a typed *LogError — in practice this never happens for a
// Config produced by Load, which already rejects it at parse time; this
// guards direct callers (e.g. tests building a loggingSection by hand).
func NewLogger(w io.Writer, cfg loggingSection) (*Logger, error) {
	level, err := parseLogLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	levelVar := &slog.LevelVar{}
	levelVar.Set(level)

	opts := &slog.HandlerOptions{Level: levelVar}
	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return &Logger{levelVar: levelVar, slog: slog.New(handler)}, nil
}

// Slog returns the wrapped *slog.Logger for callers to log through.
func (l *Logger) Slog() *slog.Logger { return l.slog }

// SetLevel updates the active handler's minimum level without
// reconstructing the handler or restarting the process (08 §3:
// [logging] is fully hot-reloadable).
func (l *Logger) SetLevel(level slog.Level) { l.levelVar.Set(level) }

// parseLogLevel maps an 08 §3 [logging].level string to a slog.Level.
func parseLogLevel(level string) (slog.Level, error) {
	switch level {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, &LogError{Field: "logging.level", Reason: fmt.Sprintf("unrecognised level %q", level)}
	}
}

// LogProvider holds the constructed *slog.Logger and its RotatingWriter
// (T-2 contract task 3), built once by Bootstrap from *Config and
// PathProvider. Later subsystems receive a LogProvider — or just its
// .Logger() — via constructor injection, never a package-global.
type LogProvider struct {
	logger   *Logger
	rotation *RotatingWriter
}

// NewLogProvider opens paths' log file (creating its directory as
// needed), wraps it in a RotatingWriter configured from cfg.Rotation
// (R-14.107: disabled unless both keys are set), and builds the slog
// Logger on top. clock drives the rotation-event line's timestamp
// deterministically in tests (Art.7.3); production callers pass
// NewSystemClock() (Bootstrap does this automatically when its own Clock
// option is nil).
func NewLogProvider(cfg loggingSection, paths PathProvider, clock Clock) (*LogProvider, error) {
	rw, err := NewRotatingWriter(LogFilePath(paths), cfg.Rotation, clock)
	if err != nil {
		return nil, err
	}
	logger, err := NewLogger(rw, cfg)
	if err != nil {
		_ = rw.Close()
		return nil, err
	}
	return &LogProvider{logger: logger, rotation: rw}, nil
}

// Logger returns the constructed *slog.Logger.
func (p *LogProvider) Logger() *slog.Logger { return p.logger.Slog() }

// SetLevel updates the active handler's level (08 §3 hot-reload).
func (p *LogProvider) SetLevel(level slog.Level) { p.logger.SetLevel(level) }

// Reconfigure updates the rotation writer's MaxSizeMB/MaxFiles (08 §3
// hot-reload).
func (p *LogProvider) Reconfigure(maxSizeMB, maxFiles int) {
	p.rotation.Reconfigure(maxSizeMB, maxFiles)
}

// Close flushes and closes the underlying log file.
func (p *LogProvider) Close() error { return p.rotation.Close() }
