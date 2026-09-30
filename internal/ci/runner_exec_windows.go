//go:build windows

// Purpose: the windows half of the local CI gate's timeout: the step's
// whole process tree is confined to a Job Object and reaped on the
// deadline, before Run returns.
//
// HOW THE TREE IS REAPED. The shell is created with CREATE_SUSPENDED (and
// CREATE_NEW_PROCESS_GROUP), assigned to a Job Object with
// AssignProcessToJobObject, and only then resumed, so nothing it starts
// can escape the job. The job carries JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
// so closing it kills any member still running: no step process outlives
// the step, the executor, or a daemon crash. On the deadline each reap
// round lists the members (JobObjectBasicProcessIdList), opens each with
// SYNCHRONIZE, calls TerminateJobObject, and waits on those handles with
// WaitForMultipleObjects. A process handle is signaled only after the
// kernel has run the process down, so when the job is empty the step's
// working directory is no longer held. taskkill /T is rejected: it would
// add a second uncontrolled sub-process to the path that just failed to
// bound the first. The ordering (assign before resume, never resume after
// kill) lives in runner_exec_tree.go; this file holds only Win32 calls.
//
// Inputs/Outputs: as runner_exec_tree_other.go; every failure is
// KindUnavailable.
// Constraints: fail closed. A job that cannot be created, limited or
// assigned never lets the step run unconfined.
// SPORT: internal.ci.processTree/ADDED, internal.ci.winJob/ADDED.

package ci

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/acamarata/cascade/pkg/cascade"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP. Named rather than
// inlined so the flag's meaning is readable without a Win32 reference.
const createNewProcessGroup = 0x00000200

// treeReapBudget bounds one round's wait for the job's members. It has
// pipeDrainGrace's value but is its own constant, so a change to one does
// not silently move the other.
const treeReapBudget = 500 * time.Millisecond

// treeReapRounds bounds the reap rounds waitTreeGone may run.
const treeReapRounds = 4

// maxWaitObjects mirrors MAXIMUM_WAIT_OBJECTS, which x/sys does not define.
const maxWaitObjects = 64

// idListHeader is the byte offset of ProcessIdList in
// JOBOBJECT_BASIC_PROCESS_ID_LIST (two DWORDs precede it on every arch);
// x/sys does not define that struct, so it is mirrored over a []uintptr.
const idListHeader = 8

// notSuspendedMsg is the fail-closed refusal for a shell that was not
// found suspended exactly once.
const notSuspendedMsg = "ci: the step's shell was not started suspended"

// setProcessGroup starts the command suspended, as its own group leader.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup | windows.CREATE_SUSPENDED
}

// processTree is one step's Job Object and its ordering core.
type processTree struct {
	jt *jobTree
	w  *winJob
}

// newProcessTree creates the job and sets its kill-on-close limit.
func newProcessTree() (processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return processTree{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: creating the step's job object")
	}
	w := &winJob{job: job}
	jt, err := newJobTree(w)
	if err != nil {
		return processTree{}, err
	}
	return processTree{jt: jt, w: w}, nil
}

// attach confines the started (suspended) shell, then resumes it.
func (t processTree) attach(cmd *exec.Cmd) error { return t.jt.attach(cmd.Process) }

// kill reaps the whole tree and returns once the job has no member.
func (t processTree) kill(cmd *exec.Cmd) error { return t.jt.kill(cmd.Process, treeReapRounds) }

// close releases the job; the kill-on-close limit kills any survivor.
func (t processTree) close() { _ = t.jt.close() }

// winJob is the Win32 jobOps: the job handle and the member handles held
// between openMembers and waitMembers.
type winJob struct {
	job     windows.Handle
	members []windows.Handle
}

// limitKillOnClose sets JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
func (w *winJob) limitKillOnClose() error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err := windows.SetInformationJobObject(w.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

// assign puts pid into the job.
func (w *winJob) assign(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.AssignProcessToJobObject(w.job, h)
}

// resume resumes pid's only thread, which must have been suspended once.
func (w *winJob) resume(pid int) error {
	tid, err := onlyThreadOf(uint32(pid))
	if err != nil {
		return err
	}
	th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, notSuspendedMsg)
	}
	defer func() { _ = windows.CloseHandle(th) }()
	prev, err := windows.ResumeThread(th)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, notSuspendedMsg)
	}
	if prev != 1 {
		return cascade.New(cascade.KindUnavailable, notSuspendedMsg)
	}
	return nil
}

// onlyThreadOf returns the id of pid's single thread; zero or several
// threads mean the process was not freshly created suspended.
func onlyThreadOf(pid uint32) (uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, notSuspendedMsg)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	var tid uint32
	found := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID == pid {
			tid = te.ThreadID
			found++
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, notSuspendedMsg)
	}
	if found != 1 {
		return 0, cascade.New(cascade.KindUnavailable, notSuspendedMsg)
	}
	return tid, nil
}

// killChild kills the step's own process only.
func (w *winJob) killChild(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// openMembers opens a SYNCHRONIZE handle on every listed member and
// returns how many it opened; a member already gone is skipped.
func (w *winJob) openMembers() (int, error) {
	ids, err := w.listMembers()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(id))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			w.closeMembers()
			return 0, err
		}
		w.members = append(w.members, h)
	}
	return len(w.members), nil
}

// listMembers queries JobObjectBasicProcessIdList, growing the buffer on
// ERROR_MORE_DATA.
func (w *winJob) listMembers() ([]uintptr, error) {
	word := int(unsafe.Sizeof(uintptr(0)))
	head := idListHeader / word
	room := maxWaitObjects
	for {
		buf := make([]uintptr, head+room)
		err := windows.QueryInformationJobObject(w.job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)*word), nil)
		counts := (*[2]uint32)(unsafe.Pointer(&buf[0]))
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			room = max(int(counts[0]), room) + maxWaitObjects
			continue
		}
		if err != nil {
			return nil, err
		}
		return buf[head : head+int(counts[1])], nil
	}
}

// terminate kills every member of the job.
func (w *winJob) terminate() error { return windows.TerminateJobObject(w.job, 1) }

// waitMembers waits for every held member, in chunks of maxWaitObjects,
// closing each chunk's handles after its wait. A WAIT_TIMEOUT is not an
// error: the next round re-lists the job.
func (w *winJob) waitMembers() error {
	for len(w.members) > 0 {
		k := min(len(w.members), maxWaitObjects)
		chunk := w.members[:k]
		_, err := windows.WaitForMultipleObjects(chunk, true, uint32(treeReapBudget/time.Millisecond))
		closeHandles(chunk)
		w.members = w.members[k:]
		if err != nil {
			w.closeMembers()
			return err
		}
	}
	w.members = nil
	return nil
}

// closeMembers closes any member handle still held.
func (w *winJob) closeMembers() {
	closeHandles(w.members)
	w.members = nil
}

// close closes held member handles, then the job itself.
func (w *winJob) close() error {
	w.closeMembers()
	return windows.CloseHandle(w.job)
}

// closeHandles closes each handle, ignoring errors (best-effort release).
func closeHandles(hs []windows.Handle) {
	for _, h := range hs {
		_ = windows.CloseHandle(h)
	}
}
