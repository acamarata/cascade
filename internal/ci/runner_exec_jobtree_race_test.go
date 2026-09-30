// Purpose: proves jobTree.attach and jobTree.kill never interleave. A fake
// jobOps blocks inside assign while kill runs; kill must touch no jobOps
// method until attach has returned, and then see the step as attached.
// SPORT: internal.ci.jobTree/TESTED.
package ci

import (
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

// killObserveWindow is how long the test holds assign blocked while
// watching for kill to reach the job. Correct code gives no event for
// "kill is waiting on the lock", so absence can only be observed over a
// bounded window; a broken lock reaches the job within microseconds.
const killObserveWindow = 200 * time.Millisecond

// blockingJob is a jobOps whose assign blocks until release is closed.
// Its own mutex guards the event list, so it is safe across goroutines.
// killChild and openMembers record an interleave when assign is in flight.
type blockingJob struct {
	mu            sync.Mutex
	events        []string
	inAssign      bool
	interleaved   bool
	assignEntered chan struct{}
	release       chan struct{}
	killTouched   chan struct{}
	touchOnce     sync.Once
}

func newBlockingJob() *blockingJob {
	return &blockingJob{
		assignEntered: make(chan struct{}),
		release:       make(chan struct{}),
		killTouched:   make(chan struct{}),
	}
}

func (b *blockingJob) record(name string) {
	b.mu.Lock()
	b.events = append(b.events, name)
	b.mu.Unlock()
}

// touch records a kill-side call and flags it if assign is in flight.
func (b *blockingJob) touch(name string) {
	b.mu.Lock()
	b.events = append(b.events, name)
	if b.inAssign {
		b.interleaved = true
		b.touchOnce.Do(func() { close(b.killTouched) })
	}
	b.mu.Unlock()
}

func (b *blockingJob) assign(int) error {
	b.mu.Lock()
	b.events = append(b.events, "assign")
	b.inAssign = true
	b.mu.Unlock()
	close(b.assignEntered)
	<-b.release
	b.mu.Lock()
	b.inAssign = false
	b.mu.Unlock()
	return nil
}

func (b *blockingJob) limitKillOnClose() error { return nil }
func (b *blockingJob) resume(int) error        { b.record("resume"); return nil }
func (b *blockingJob) terminate() error        { b.record("terminate"); return nil }
func (b *blockingJob) waitMembers() error      { b.record("wait"); return nil }
func (b *blockingJob) close() error            { b.record("close"); return nil }

func (b *blockingJob) killChild(*os.Process) error { b.touch("killChild"); return nil }
func (b *blockingJob) openMembers() (int, error)   { b.touch("open"); return 0, nil }

func TestJobTreeKillWaitsForAttach(t *testing.T) {
	job := newBlockingJob()
	jt, err := newJobTree(job)
	if err != nil {
		t.Fatalf("newJobTree: %v", err)
	}
	child := &os.Process{Pid: 4242}
	var attachErr, killErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); attachErr = jt.attach(child) }()
	<-job.assignEntered
	go func() { defer wg.Done(); killErr = jt.kill(child, 4) }()

	select {
	case <-job.killTouched:
		close(job.release)
		wg.Wait()
		t.Fatal("kill reached the job while attach was still assigning")
	case <-time.After(killObserveWindow):
	}
	close(job.release)
	wg.Wait()

	job.mu.Lock()
	defer job.mu.Unlock()
	if attachErr != nil || killErr != nil {
		t.Fatalf("attach = %v, kill = %v, want nil, nil", attachErr, killErr)
	}
	if job.interleaved {
		t.Error("a kill-side call ran while assign was in flight")
	}
	want := []string{"assign", "resume", "open"}
	if !reflect.DeepEqual(job.events, want) {
		t.Errorf("events = %v, want %v", job.events, want)
	}
}
