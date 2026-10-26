//go:build linux

package agents

// Purpose: the linux peer inspector: environment from /proc/<pid>/environ,
// parent, process group and session from /proc/<pid>/stat.
//
// Constraints: unlike the lease sweep's reader, a denied or vanished process is
// an error here, never "no entries": the guard treats it as a refusal. A
// process that made itself non-dumpable (PR_SET_DUMPABLE 0) has an unreadable
// environ and is therefore refused too. An environ larger than maxEnvironBytes
// is an error.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxEnvironBytes bounds one environ read; the kernel caps a process's
// argument and environment block far below this.
const maxEnvironBytes = 8 << 20

// procInspector reads a procfs tree.
type procInspector struct{ root string }

// platformPeerInspector is the /proc inspector.
func platformPeerInspector() (peerInspector, error) { return procInspector{root: procRoot}, nil }

// errPeerExited refuses a peer that has exited (zombie or dead state): its
// environ reads empty, so an unmatched read would prove nothing.
var errPeerExited = errors.New("agents: the socket peer has exited")

// alive fails unless /proc/<pid>/stat shows a state other than Z or X.
func (p procInspector) alive(pid int) error {
	raw, err := os.ReadFile(p.root + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return err
	}
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return errStatShape
	}
	fields := bytes.Fields(raw[i+1:])
	if len(fields) == 0 {
		return errStatShape
	}
	if st := string(fields[0]); st == "Z" || st == "X" || st == "x" {
		return errPeerExited
	}
	return nil
}

// holds reads /proc/<pid>/environ, bracketed by liveness checks so that an
// exited peer's empty environ is never read as "no marker".
func (p procInspector) holds(pid int, entries []string) (bool, error) {
	if err := p.alive(pid); err != nil {
		return false, err
	}
	held, err := p.readHolds(pid, entries)
	if err != nil {
		return false, err
	}
	if err := p.alive(pid); err != nil {
		return false, err
	}
	return held, nil
}

// readHolds is holds without the liveness bracket.
func (p procInspector) readHolds(pid int, entries []string) (bool, error) {
	f, err := os.Open(p.root + "/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	blob, err := io.ReadAll(io.LimitReader(f, maxEnvironBytes+1))
	if err != nil {
		return false, err
	}
	if len(blob) > maxEnvironBytes {
		return false, cascade.New(cascade.KindInvalidInput, "agents: environ larger than the read limit")
	}
	for _, e := range entries {
		if environHas(blob, e) {
			return true, nil
		}
	}
	return false, nil
}

// stat returns the ppid, pgrp and session fields of /proc/<pid>/stat.
func (p procInspector) stat(pid int) (ppid, pgid, sid int, err error) {
	raw, err := os.ReadFile(p.root + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, 0, err
	}
	return parseStat(raw)
}

// errStatShape reports a /proc/<pid>/stat line that does not parse.
var errStatShape = errors.New("agents: unexpected /proc stat format")

// parseStat reads state, ppid, pgrp and session from one stat line. The
// command name sits between parentheses and may itself hold spaces and
// parentheses, so the fields start after the LAST ')'.
func parseStat(raw []byte) (ppid, pgid, sid int, err error) {
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return 0, 0, 0, errStatShape
	}
	fields := bytes.Fields(raw[i+1:])
	if len(fields) < 4 {
		return 0, 0, 0, errStatShape
	}
	vals := [3]int{}
	for k := range vals {
		if vals[k], err = strconv.Atoi(string(fields[k+1])); err != nil {
			return 0, 0, 0, errStatShape
		}
	}
	return vals[0], vals[1], vals[2], nil
}

// groups returns pid's process group and session.
func (p procInspector) groups(pid int) (int, int, error) {
	_, pgid, sid, err := p.stat(pid)
	return pgid, sid, err
}

// parent returns pid's parent.
func (p procInspector) parent(pid int) (int, error) {
	ppid, _, _, err := p.stat(pid)
	return ppid, err
}
