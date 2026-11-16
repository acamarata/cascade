package swarm

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
)

// Request represents a command to be executed by the backend.
type Request struct {
	Command         string
	Target          string
	SuggestSections bool
}

// Response represents the result of a backend execution.
type Response struct {
	Text string
}

// Backend defines the interface for executing requests.
type Backend interface {
	Execute(req Request) (Response, error)
}

// ErrBackendUnavailable is returned when no backend is configured.
var ErrBackendUnavailable = errors.New("backend unavailable")

// ErrInvalidWriter is returned when an output writer is nil.
var ErrInvalidWriter = errors.New("invalid writer")

var (
	mu      sync.RWMutex
	backend Backend
)

// ConfigureBackend replaces the package configuration atomically.
// Passing nil or a typed nil clears the backend.
func ConfigureBackend(b Backend) {
	mu.Lock()
	defer mu.Unlock()
	if b == nil {
		backend = nil
		return
	}
	v := reflect.ValueOf(b)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		if v.IsNil() {
			backend = nil
			return
		}
	}
	backend = b
}

func parseArgs(args []string) (cmd, target string, suggest bool, err error) {
	if len(args) < 2 || len(args) > 3 {
		return "", "", false, errors.New("invalid args")
	}

	validCmds := map[string]bool{
		"gen": true, "ready": true, "pack": true, "fix": true, "layout": true, "suggest-sections": true,
	}

	cmd = args[0]
	if !validCmds[cmd] {
		return "", "", false, errors.New("invalid cmd")
	}

	target = strings.TrimSpace(args[1])
	if target == "" || strings.HasPrefix(target, "-") {
		return "", "", false, errors.New("invalid target")
	}

	if len(args) == 3 {
		if args[2] == "--suggest-sections" {
			suggest = true
		} else {
			return "", "", false, errors.New("invalid flag")
		}
	}

	return cmd, target, suggest, nil
}

// Run dispatches the command based on arguments, executes the backend, and writes the response.
func Run(args []string, out, errw io.Writer) int {
	if out == nil {
		if errw != nil {
			io.WriteString(errw, "pbd: invalid writer\n")
		}
		return 1
	}

	cmd, target, suggest, err := parseArgs(args)
	if err != nil {
		if errw != nil {
			io.WriteString(errw, "usage: capp pbd {gen|ready|pack|fix|layout|suggest-sections} TARGET [--suggest-sections]\n")
		}
		return 2
	}

	mu.RLock()
	b := backend
	mu.RUnlock()

	if b == nil {
		if errw != nil {
			io.WriteString(errw, "pbd: backend unavailable\n")
		}
		return 1
	}

	req := Request{
		Command:         cmd,
		Target:          target,
		SuggestSections: suggest,
	}

	resp, err := b.Execute(req)
	if err != nil {
		if errw != nil {
			io.WriteString(errw, "pbd: "+err.Error()+"\n")
		}
		return 1
	}

	io.WriteString(out, resp.Text)
	return 0
}
