// Purpose: jobTree's ordering contract, on every OS, through a recording
// fake jobOps (no process, no sleep): assign before resume, a failed
// attach kills the still-suspended child and never resumes it, a kill
// before attach is final, and a kill after attach terminates and waits
// round by round until the job is empty.
// SPORT: internal.ci.jobTree/TESTED.
package ci

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// fakeJob records every jobOps call in order. errs fails a named call;
// members is what successive openMembers calls report.
type fakeJob struct {
	calls   []string
	errs    map[string]error
	members []int
	killed  *os.Process
}

func (f *fakeJob) rec(name string) error {
	f.calls = append(f.calls, name)
	return f.errs[name]
}

func (f *fakeJob) limitKillOnClose() error { return f.rec("limit") }
func (f *fakeJob) assign(int) error        { return f.rec("assign") }
func (f *fakeJob) resume(int) error        { return f.rec("resume") }
func (f *fakeJob) terminate() error        { return f.rec("terminate") }
func (f *fakeJob) waitMembers() error      { return f.rec("wait") }
func (f *fakeJob) close() error            { return f.rec("close") }

func (f *fakeJob) killChild(p *os.Process) error {
	f.killed = p
	return f.rec("killChild")
}

func (f *fakeJob) openMembers() (int, error) {
	n := f.members[0]
	f.members = f.members[1:]
	return n, f.rec("open")
}

// newFakeTree builds a jobTree over f and clears the constructor's call.
func newFakeTree(t *testing.T, f *fakeJob) *jobTree {
	t.Helper()
	jt, err := newJobTree(f)
	if err != nil {
		t.Fatalf("newJobTree: %v", err)
	}
	f.calls = nil
	return jt
}

func wantCalls(t *testing.T, f *fakeJob, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

// errJobTreeInjected is the injected failure every error case wraps.
var errJobTreeInjected = errors.New("injected")

// TestJobTree runs jobTree's ordering cases; each is a named function so
// every case stays small.
func TestJobTree(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, child *os.Process)
	}{
		{"assign precedes resume", jobTreeAttachOrder},
		{"assign error kills the child and never resumes", jobTreeAssignError},
		{"resume error kills the child", jobTreeResumeError},
		{"kill before attach is final", jobTreeKillBeforeAttach},
		{"kill after attach reaps round by round", jobTreeKillAfterAttach},
		{"terminate error", jobTreeTerminateError},
		{"limit error closes the job", jobTreeLimitError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, &os.Process{Pid: 4242}) })
	}
}

func jobTreeAttachOrder(t *testing.T, child *os.Process) {
	f := &fakeJob{}
	if err := newFakeTree(t, f).attach(child); err != nil {
		t.Fatalf("attach: %v", err)
	}
	wantCalls(t, f, "assign", "resume")
}

func jobTreeAssignError(t *testing.T, child *os.Process) {
	f := &fakeJob{errs: map[string]error{"assign": errJobTreeInjected}}
	err := newFakeTree(t, f).attach(child)
	wantCalls(t, f, "assign", "killChild")
	checkTreeErr(t, err, errJobTreeInjected, "ci: assigning the step to its job object")
}

func jobTreeResumeError(t *testing.T, child *os.Process) {
	f := &fakeJob{errs: map[string]error{"resume": errJobTreeInjected}}
	err := newFakeTree(t, f).attach(child)
	wantCalls(t, f, "assign", "resume", "killChild")
	checkTreeErr(t, err, errJobTreeInjected, "ci: resuming the step's suspended shell")
}

func jobTreeKillBeforeAttach(t *testing.T, child *os.Process) {
	f := &fakeJob{}
	jt := newFakeTree(t, f)
	if err := jt.kill(child, 4); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if f.killed != child {
		t.Errorf("killChild got %p, want the step's own process %p", f.killed, child)
	}
	if err := jt.attach(child); err != nil {
		t.Fatalf("attach after kill: %v", err)
	}
	wantCalls(t, f, "killChild")
}

func jobTreeKillAfterAttach(t *testing.T, child *os.Process) {
	f := &fakeJob{members: []int{2, 1, 0}}
	jt := newFakeTree(t, f)
	if err := jt.attach(child); err != nil {
		t.Fatalf("attach: %v", err)
	}
	f.calls = nil
	if err := jt.kill(child, 4); err != nil {
		t.Fatalf("kill: %v", err)
	}
	wantCalls(t, f, "open", "terminate", "wait", "open", "terminate", "wait", "open")
}

func jobTreeTerminateError(t *testing.T, child *os.Process) {
	f := &fakeJob{members: []int{1}, errs: map[string]error{"terminate": errJobTreeInjected}}
	jt := newFakeTree(t, f)
	if err := jt.attach(child); err != nil {
		t.Fatalf("attach: %v", err)
	}
	checkTreeErr(t, jt.kill(child, 4), errJobTreeInjected, "ci: reaping the timed-out step's process tree")
}

func jobTreeLimitError(t *testing.T, _ *os.Process) {
	f := &fakeJob{errs: map[string]error{"limit": errJobTreeInjected}}
	jt, err := newJobTree(f)
	if jt != nil {
		t.Error("newJobTree returned a tree without the kill-on-close limit")
	}
	wantCalls(t, f, "limit", "close")
	checkTreeErr(t, err, errJobTreeInjected, "ci: limiting the step's job to kill on close")
}
