//go:build !windows

// Purpose: the POSIX process-group surface (R-21.174/R-21.177): Setpgid at
//   exec, the strict process-group lookup, group-wide signals, the
//   group-empty test, O_NOFOLLOW, and the straggler environment audit.
// Inputs: an *exec.Cmd, a pid or pgid, a signal, a vendor variable name.
// Outputs: typed errors, never panics; audit verdicts.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add;
//   process-group control and the open flags come from pkg/procgroup and
//   pkg/nofollow (the agent-driver net-surface gate bans syscall here);
//   kill(2) group semantics are unix-only, so the windows build needs a
//   twin (internal/jobs/lease_fence_unix.go is the in-tree precedent). A
//   group is empty only on ESRCH: EPERM (a zombie, or a live member this
//   process may not signal) and every other error mean "not confirmed
//   yet". The audit reads /proc on linux; darwin does not expose another
//   process's environment (kern.procargs2 omits it for same-user
//   processes), so there a signalable member counts at once and an
//   unsignalable group counts unless it empties within a short wait.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2), deviation.

package claude

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/nofollow"
	"github.com/acamarata/cascade/pkg/procgroup"
)

// openNoFollow refuses to open a symlink as the final path component;
// openNonBlock keeps an open of a FIFO from blocking.
const (
	openNoFollow = nofollow.NoFollow
	openNonBlock = nofollow.NonBlock
)

// getpgid is the process-group lookup; a variable so a test can make the
// lookup fail and prove Spawn refuses.
var getpgid = procgroup.Lookup

// setProcessGroup makes the child its own group leader (on linux the child
// also gets SIGKILL when the daemon thread that started it dies).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = procgroup.Attr()
}

// resolvePgid returns the child's own process group. A group other than
// its own, or a failed lookup, refuses. A child that already exited is a
// zombie whose getpgid reports ESRCH on darwin; its group is confirmed
// through kill(-pid, 0) instead, which still sees the zombie.
func resolvePgid(pid int) (int, error) {
	pgid, err := getpgid(pid)
	switch {
	case err == nil && pgid == pid && pid > 1:
		return pgid, nil
	case err == nil:
		return 0, cascade.Newf(cascade.KindInternal, "claude: child %d is in group %d, not its own", pid, pgid)
	case procgroup.IsNoSuchProcess(err) && pid > 1 && procgroup.Exists(pid):
		return pid, nil
	}
	return 0, cascade.Wrapf(cascade.KindInternal, err, "claude: looking up the child's process group")
}

// signalGroup sends sig to every member of pgid's group. ESRCH (the group
// is already gone) is success. pgid 1 is refused: kill(-1) would signal
// every process this user may signal, not one group.
func signalGroup(pgid int, sig procgroup.Sig) error {
	return procgroup.Signal(pgid, sig)
}

// groupIsEmpty is true only when kill(-pgid, 0) reports ESRCH.
func groupIsEmpty(pgid int) bool {
	return procgroup.Empty(pgid)
}

// zombieRecheck bounds how long the darwin audit waits for a group whose
// members it may not signal to empty.
const zombieRecheck = 50 * time.Millisecond

// auditStragglers reports whether any live member of pgid carries
// one of vendorVars (the config-dir variable or the key variable), or
// cannot be proven not to.
func auditStragglers(pgid int, vendorVars []string) bool {
	if runtime.GOOS != "linux" {
		// Any member counts, EPERM included: it is either a live member
		// this process may not signal or a zombie, and neither can be
		// audited here, so the run cannot be proven clean. A member that
		// exited at the end of the settle is a zombie until launchd reaps
		// it, so an unsignalable group gets a short bounded wait to empty.
		if !procgroup.Exists(pgid) {
			return false
		}
		// Signal reads ESRCH as nil, so a group that emptied since Exists
		// must not count as a signalable member.
		if procgroup.Signal(pgid, 0) == nil && !procgroup.Empty(pgid) {
			return true
		}
		recheck, cancel := context.WithTimeout(context.Background(), zombieRecheck)
		defer cancel()
		return !waitGroupEmpty(recheck, pgid)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil || !liveMember(pid, pgid) {
			continue
		}
		env, readErr := os.ReadFile("/proc/" + e.Name() + "/environ")
		switch {
		case os.IsNotExist(readErr):
			continue // exited since the listing
		case readErr != nil || hasAnyEnvVar(env, vendorVars):
			return true
		}
	}
	return false
}

// liveMember reads /proc/<pid>/stat and reports a non-zombie in pgid.
func liveMember(pid, pgid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return false
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 3 || fields[0] == "Z" {
		return false
	}
	g, err := strconv.Atoi(fields[2])
	return err == nil && g == pgid
}

// hasAnyEnvVar reports whether environ holds any of names.
func hasAnyEnvVar(environ []byte, names []string) bool {
	for _, name := range names {
		if name != "" && hasEnvVar(environ, name) {
			return true
		}
	}
	return false
}

func hasEnvVar(environ []byte, name string) bool {
	for _, kv := range bytes.Split(environ, []byte{0}) {
		if bytes.HasPrefix(kv, []byte(name+"=")) {
			return true
		}
	}
	return false
}
