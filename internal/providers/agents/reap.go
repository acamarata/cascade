package agents

// Purpose: the lease-marker process sweep. Every driver spawn carries a
// fresh CASCADE_LEASE_ID=<marker> in its environment; at lease end
// SweepLease kills every same-uid process whose environment still holds that
// exact entry, setsid descendants and orphans included, then lists again and
// fails closed if any is left.
//
// Constraints: the marker is the identity: a match is one exact environ
// entry, never a substring, a name or a process group. Processes of another
// uid are never listed or signalled. A process that execs with a scrubbed
// environment, or rewrites its own initial environment block, leaves the
// sweep's reach, and so does one that makes its environment unreadable
// (permission denied); both are the residual DEF-P2-driver-tool-isolation, see
// docs/security-posture/driver-containment.md. On macOS the kernel also
// withholds the environment of Apple-signed system binaries (same page). Any
// other failure to inspect a process fails the sweep closed. Success always
// follows an empty re-list.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LeaseMarkerEnv is the environment variable that carries a spawn's marker.
const LeaseMarkerEnv = "CASCADE_LEASE_ID"

const (
	markerPrefix = "clm_"
	markerHexLen = 32
	sweepRounds  = 40                    // kill/re-list rounds before giving up
	sweepPause   = 50 * time.Millisecond // settle time between rounds
)

// ErrLeaseSweepIncomplete reports a marked process still running after the
// sweep's last re-list.
var ErrLeaseSweepIncomplete = cascade.New(cascade.KindUnavailable, "agent: lease sweep left a marked process running")

// NewLeaseMarker returns "clm_" plus 32 hex characters from crypto/rand.
func NewLeaseMarker() (string, error) { return newLeaseMarker(rand.Reader) }

// newLeaseMarker is NewLeaseMarker over an explicit entropy source.
func newLeaseMarker(r io.Reader) (string, error) {
	b := make([]byte, markerHexLen/2)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "agents: the lease marker could not be generated")
	}
	return markerPrefix + hex.EncodeToString(b), nil
}

// leaseEntry is the one environment entry that carries marker: the string a
// spawn puts in the child's environment and the sweep matches whole.
func leaseEntry(marker string) string { return LeaseMarkerEnv + "=" + marker }

// validMarker reports whether m has exactly the NewLeaseMarker shape, so an
// empty or short marker can never widen the match.
func validMarker(m string) bool {
	if len(m) != len(markerPrefix)+markerHexLen || m[:len(markerPrefix)] != markerPrefix {
		return false
	}
	for i := len(markerPrefix); i < len(m); i++ {
		if c := m[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// procSource is the per-OS view of the process table.
type procSource interface {
	// list returns the pids of this uid's processes.
	list() ([]int, error)
	// hasEntry reports whether pid's environment holds entry exactly. A
	// process that is gone, a zombie or denied has no entries (nil error);
	// any other failure to inspect is returned so the sweep fails closed.
	hasEntry(pid int, entry string) (bool, error)
	// kill sends SIGKILL to pid only if pid still carries entry when the
	// signal is committed, so a recycled pid is never killed. A process
	// already gone is not an error.
	kill(pid int, entry string) error
}

// noEntries reports whether err only means "this process shows no
// environment": it is gone, a zombie, or denied to us. Any other error is an
// inspection failure.
func noEntries(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EINVAL)
}

// sweeper is one sweep's process source and pacing.
type sweeper struct {
	src    procSource
	rounds int
	pause  time.Duration
}

// SweepLease kills every same-uid process carrying marker and returns nil
// only when a re-list afterwards finds none. A survivor after the last round
// is ErrLeaseSweepIncomplete; an unlistable process table is KindUnavailable;
// windows is KindUnsupported.
func SweepLease(ctx context.Context, marker string) error {
	if !validMarker(marker) {
		return cascade.New(cascade.KindInvalidInput, "agents: lease marker is not a NewLeaseMarker value")
	}
	src, err := platformSource()
	if err != nil {
		return err
	}
	return sweeper{src: src, rounds: sweepRounds, pause: sweepPause}.sweep(ctx, marker)
}

// sweep alternates kill and re-list until the list is empty. A process whose
// environment could not be read this round is retried next round and only
// fails the sweep if it still cannot be read at the last one.
func (s sweeper) sweep(ctx context.Context, marker string) error {
	entry := leaseEntry(marker)
	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return cascade.Wrap(cascade.KindCanceled, err, "agents: lease sweep canceled")
		}
		pids, unsure, err := s.marked(ctx, entry)
		if err != nil {
			return err
		}
		if len(pids) == 0 && unsure == nil {
			return nil
		}
		if round >= s.rounds {
			if len(pids) > 0 {
				return ErrLeaseSweepIncomplete
			}
			return cascade.Wrap(cascade.KindUnavailable, unsure, "agents: a process environment could not be inspected")
		}
		for _, pid := range pids {
			// The source re-checks the entry as it commits the signal, so a
			// pid recycled since the listing is not killed; a kill that
			// failed shows up as a survivor in the re-list.
			_ = s.src.kill(pid, entry)
		}
		if s.pause > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(s.pause):
			}
		}
	}
}

// marked lists the pids, other than this process, whose environment holds
// entry. unsure is the last error from a process that could not be inspected
// (nil when every process could be).
func (s sweeper) marked(ctx context.Context, entry string) (out []int, unsure error, err error) {
	all, err := s.src.list()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "agents: the process table could not be listed")
	}
	self := os.Getpid()
	for _, pid := range all {
		if pid <= 1 || pid == self {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, cascade.Wrap(cascade.KindCanceled, err, "agents: lease sweep canceled")
		}
		has, err := s.src.hasEntry(pid, entry)
		if err != nil {
			unsure = err
		} else if has {
			out = append(out, pid)
		}
	}
	return out, unsure, nil
}

// environHas reports whether blob, NUL-separated KEY=VALUE entries, holds
// entry as one whole entry.
func environHas(blob []byte, entry string) bool {
	want := []byte(entry)
	for len(blob) > 0 {
		cur := blob
		if i := bytes.IndexByte(blob, 0); i >= 0 {
			cur, blob = blob[:i], blob[i+1:]
		} else {
			blob = nil
		}
		if bytes.Equal(cur, want) {
			return true
		}
	}
	return false
}

// procArgsHas reports whether a KERN_PROCARGS2 buffer (int32 argc, the exec
// path, NUL padding, argc argument strings, then the environment strings)
// holds entry as a whole environment entry. The kernel pads the path and its
// terminator to 8 bytes, so the NULs beyond that padding are leading empty
// arguments and the environment starts exactly argc strings in. If the run is
// shorter than that padding the layout is not the one measured, and every
// string from argc minus the run's length onward is accepted, so an evader is
// never missed. One pass, no quadratic rescans: a hostile process can make
// this buffer large.
func procArgsHas(buf []byte, entry string) bool {
	if len(buf) < 4 {
		return false
	}
	argc := int(binary.NativeEndian.Uint32(buf[:4]))
	rest := buf[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return false
	}
	after := rest[i+1:] // past the exec path's terminator
	strs := bytes.TrimLeft(after, "\x00")
	empty := len(after) - len(strs)
	if pad := (8 - (i+1)%8) % 8; empty >= pad {
		empty -= pad
	}
	first := argc - min(empty, argc) // earliest string that can start the environment
	want := []byte(entry)
	for k := 0; len(strs) > 0; k++ {
		cur := strs
		if j := bytes.IndexByte(strs, 0); j >= 0 {
			cur, strs = strs[:j], strs[j+1:]
		} else {
			strs = nil
		}
		if k >= first && bytes.Equal(cur, want) {
			return true
		}
	}
	return false
}
