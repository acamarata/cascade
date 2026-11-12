// Package graph provides node-side planning and execution helpers for capp
// commands. This file classifies commands by whether they need the capp
// daemon, so the node runtime can run read-only commands daemon-free.
package graph

import (
	"fmt"
	"strings"
)

// writeFlag marks a command invocation that mutates node state. Daemon-free
// classification is defined for read-only invocations only, so the flag is
// rejected outright.
const writeFlag = "--write"

// daemonFreeCommands lists the command names that the node runtime can serve
// without the capp daemon. Two-word commands such as "pbd check" are stored
// as a single space-separated key.
var daemonFreeCommands = map[string]bool{
	"pbd check":        true,
	"ready":            true,
	"status":           true,
	"waves":            true,
	"graph":            true,
	"trace":            true,
	"pack":             true,
	"suggest-sections": true,
	"version":          true,
}

// NeedsHost reports whether the given capp command must be sent to the capp
// daemon (host) or can be executed daemon-free by the node runtime.
//
// cmd is the command verb; for two-word commands cmd is the first word and
// args[0] the second, so "pbd check" arrives as cmd "pbd" with args
// ["check"]. Any occurrence of the writeFlag in args is an error because
// daemon-free commands are read-only. The special command "batch" carries one
// command line per argument, for example "pbd check"; a batch must be
// homogeneous, so mixing daemon-free and daemon-requiring lines is an error.
// Unknown commands conservatively report true so they are routed to the
// daemon.
//
// NeedsHost is pure classification: it never starts a process, touches the
// network, or mutates any state.
func NeedsHost(cmd string, args []string) (bool, error) {
	if err := checkNoWriteFlag(args); err != nil {
		return false, err
	}
	if cmd == "batch" {
		return needsHostBatch(args)
	}
	return !isDaemonFree(commandName(cmd, args)), nil
}

// needsHostBatch classifies a batch of command lines. Every line is parsed
// and classified independently; the batch is daemon-free only if all lines
// are. Mixing daemon-free and daemon-requiring lines returns an error, as
// does a writeFlag inside any line.
func needsHostBatch(lines []string) (bool, error) {
	sawFree := false
	sawHost := false
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if err := checkNoWriteFlag(fields); err != nil {
			return false, err
		}
		if isDaemonFree(commandName(fields[0], fields[1:])) {
			sawFree = true
		} else {
			sawHost = true
		}
		if sawFree && sawHost {
			return false, fmt.Errorf("batch mixes daemon-free and daemon-requiring commands")
		}
	}
	// An empty or host-only batch is routed to the daemon.
	return sawHost || !sawFree, nil
}

// commandName rebuilds the canonical command name from the verb and its
// arguments, joining the first two words for two-word commands such as
// "pbd check".
func commandName(cmd string, args []string) string {
	if len(args) > 0 {
		return cmd + " " + args[0]
	}
	return cmd
}

// isDaemonFree reports whether the canonical command name can be served by
// the node runtime without the capp daemon.
func isDaemonFree(name string) bool {
	if daemonFreeCommands[name] {
		return true
	}
	// Fall back to the bare verb for commands that were passed with extra
	// arguments, for example "status --json".
	verb, _, _ := strings.Cut(name, " ")
	return daemonFreeCommands[verb]
}

// checkNoWriteFlag returns an error if the writeFlag appears anywhere in
// args, because write invocations are never daemon-free.
func checkNoWriteFlag(args []string) error {
	for _, arg := range args {
		if arg == writeFlag {
			return fmt.Errorf("flag %s is not supported for daemon-free commands", writeFlag)
		}
	}
	return nil
}
