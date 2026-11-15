package daemon

import (
	"fmt"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/waitreason"
)

// worktreeSnapshotWaitTracker remembers a worktree snapshot hold for the
// duration of one task's preparation, so a prepare timeout can say WHAT the
// task was waiting on instead of only that time ran out.
//
// The tracker is written from the acquiring goroutine (LocalPathLocker invokes
// onWait synchronously) and read from runTask's deferred timeout handler, which
// runs on the same goroutine; the mutex keeps the type safe under test and
// keeps the read honest if a future caller moves it.
type worktreeSnapshotWaitTracker struct {
	mu    sync.Mutex
	wait  *waitreason.WorktreeSnapshot
	since time.Time
}

// parking records the start of a hold. The first hold wins: a tracker belongs
// to one task, and its lock is acquired at most once.
func (t *worktreeSnapshotWaitTracker) parking(w waitreason.WorktreeSnapshot, now time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.wait != nil {
		return
	}
	t.wait = &w
	t.since = now
}

// resumed clears the hold once this task owns the lock. Clearing on acquire
// (not on start) is what keeps the timeout context honest: it is reported only
// when the deadline fired WHILE the task was still queued behind the holder.
func (t *worktreeSnapshotWaitTracker) resumed() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.wait = nil
	t.since = time.Time{}
}

// timeoutContext returns the suffix a prepare-timeout error should carry, or ""
// when this task was not waiting for a snapshot when the deadline fired.
func (t *worktreeSnapshotWaitTracker) timeoutContext(now time.Time) string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.wait == nil {
		return ""
	}
	waited := now.Sub(t.since)
	if waited < 0 {
		waited = 0
	}
	return fmt.Sprintf(" while waiting for a consistent worktree snapshot of %s (holder task %s, waited %s)",
		t.wait.LocalDirectory, t.wait.Holder, waited.Round(time.Second))
}

// prepareTimeoutError collapses every deadline shape of the preparation phase
// into the one sentinel handleTask classifies as a retryable platform timeout,
// carrying whatever the task was waiting on when the deadline fired.
//
// The suffix is derived from structured values (display name, task id) that are
// already safe for the wait_reason column, so it cannot carry a prompt, a
// credential, or user-authored text into the error a user reads.
func prepareTimeoutError(budget time.Duration, tracker *worktreeSnapshotWaitTracker, now time.Time) error {
	return fmt.Errorf("%w after %s%s", errTaskPrepareTimeout, budget, tracker.timeoutContext(now))
}
