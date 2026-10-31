package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/waitreason"
)

// waitLocalDirectoryRecorder captures the reasons the daemon POSTs to
// /wait-local-directory, so a test can assert on what the server would store
// rather than on an internal variable.
type waitLocalDirectoryRecorder struct {
	mu      chan struct{}
	reasons []string
}

func newWaitLocalDirectoryRecorder() *waitLocalDirectoryRecorder {
	r := &waitLocalDirectoryRecorder{mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	return r
}

func (r *waitLocalDirectoryRecorder) add(reason string) {
	<-r.mu
	r.reasons = append(r.reasons, reason)
	r.mu <- struct{}{}
}

func (r *waitLocalDirectoryRecorder) all() []string {
	<-r.mu
	out := append([]string(nil), r.reasons...)
	r.mu <- struct{}{}
	return out
}

// worktreeWaitServer is a fake server that records wait-local-directory calls
// and answers /status with the caller's chosen status.
func worktreeWaitServer(t *testing.T, recorder *waitLocalDirectoryRecorder, status string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wait-local-directory"):
			var body struct {
				Reason string `json:"reason"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			recorder.add(body.Reason)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/status"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func worktreeTask(t *testing.T, daemonID, taskID, dir string) Task {
	t.Helper()
	raw, err := json.Marshal(localDirectoryRef{
		LocalPath:     dir,
		DaemonID:      daemonID,
		ExecutionMode: localDirectoryModeWorktree,
	})
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	return Task{
		ID: taskID,
		ProjectResources: []ProjectResourceData{
			{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw},
		},
	}
}

// acquireWorktreeSnapshotLock mirrors the exact call the worktree branch of
// runTask makes, so the tests below exercise the production sequence (Acquire →
// onWait writes the reason → prepare → resumed) without standing up an
// execution environment.
//
// withHook, when non-nil, runs while the task owns the lock — that is where
// runTask enters prepareExecutionEnvironment.
func acquireWorktreeSnapshotLock(
	ctx context.Context,
	d *Daemon,
	task Task,
	assignment *localDirectoryAssignment,
	tracker *worktreeSnapshotWaitTracker,
	taskLog *slog.Logger,
	withHook func(),
) error {
	release, err := d.localPathLocks.Acquire(ctx, assignment.RealPath, task.ID, func(holder string) {
		wait := waitreason.WorktreeSnapshot{
			Holder:         holder,
			LocalDirectory: assignment.DisplayName(),
		}
		tracker.parking(wait, time.Now())
		if waitErr := d.client.MarkTaskWaitingLocalDirectory(ctx, task.ID, wait.String()); waitErr != nil {
			taskLog.Warn("local_directory: mark waiting status failed", "error", waitErr)
		}
	})
	if err != nil {
		return err
	}
	tracker.resumed()
	if withHook != nil {
		withHook()
	}
	release()
	return nil
}

// TestWorktreeSnapshotWaitWritesStructuredReason pins requirement 1: the hold
// is persisted in the documented shape, naming the holder and the directory,
// and never the absolute path.
func TestWorktreeSnapshotWaitWritesStructuredReason(t *testing.T) {
	t.Parallel()

	const daemonID = "d-mine"
	dir := t.TempDir()
	recorder := newWaitLocalDirectoryRecorder()
	srv := worktreeWaitServer(t, recorder, "running")

	task := worktreeTask(t, daemonID, "task-waiter", dir)
	assignment, err := localDirectoryAssignmentForTask(task, daemonID)
	if err != nil || assignment == nil {
		t.Fatalf("assignment: %v (nil=%v)", err, assignment == nil)
	}

	d := &Daemon{
		cfg:                Config{DaemonID: daemonID},
		client:             NewClient(srv.URL),
		localPathLocks:     NewLocalPathLocker(),
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		cancelPollInterval: time.Hour,
	}
	// Hold the lock from a sibling task, exactly as an in-place run would.
	release, err := d.localPathLocks.Acquire(context.Background(), assignment.RealPath, "task-holder", nil)
	if err != nil {
		t.Fatalf("preclaim: %v", err)
	}
	t.Cleanup(release)

	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		err := acquireWorktreeSnapshotLock(context.Background(), d, task, assignment,
			&worktreeSnapshotWaitTracker{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		done <- result{err: err}
	}()

	// Wait for the park, then release the holder so the waiter can proceed and
	// the goroutine does not outlive the test.
	deadline := time.Now().Add(2 * time.Second)
	for len(recorder.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	release()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("acquire: %v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worktree snapshot wait never completed after the holder released")
	}

	reasons := recorder.all()
	if len(reasons) != 1 {
		t.Fatalf("wait-local-directory calls = %d, want 1: %v", len(reasons), reasons)
	}
	parsed, ok := waitreason.ParseWorktreeSnapshot(reasons[0])
	if !ok {
		t.Fatalf("persisted reason %q does not parse as a worktree snapshot hold", reasons[0])
	}
	if parsed.Holder != "task-holder" {
		t.Errorf("holder = %q, want task-holder", parsed.Holder)
	}
	if parsed.LocalDirectory != filepath.Base(dir) {
		t.Errorf("local_directory = %q, want the display name %q", parsed.LocalDirectory, filepath.Base(dir))
	}
	if strings.Contains(reasons[0], dir) {
		t.Errorf("absolute directory path leaked into the persisted reason: %q", reasons[0])
	}
}

// TestWorktreeSnapshotNoContentionWritesNoReason pins the existing behaviour
// this change must not disturb: a worktree task that takes a free lock never
// parks, so it never writes a reason.
func TestWorktreeSnapshotNoContentionWritesNoReason(t *testing.T) {
	t.Parallel()

	const daemonID = "d-mine"
	dir := t.TempDir()
	recorder := newWaitLocalDirectoryRecorder()
	srv := worktreeWaitServer(t, recorder, "running")

	task := worktreeTask(t, daemonID, "task-solo", dir)
	assignment, err := localDirectoryAssignmentForTask(task, daemonID)
	if err != nil || assignment == nil {
		t.Fatalf("assignment: %v (nil=%v)", err, assignment == nil)
	}
	d := &Daemon{
		cfg:                Config{DaemonID: daemonID},
		client:             NewClient(srv.URL),
		localPathLocks:     NewLocalPathLocker(),
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		cancelPollInterval: time.Hour,
	}

	tracker := &worktreeSnapshotWaitTracker{}
	if err := acquireWorktreeSnapshotLock(context.Background(), d, task, assignment, tracker,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if reasons := recorder.all(); len(reasons) != 0 {
		t.Fatalf("uncontended worktree task wrote wait reasons: %v", reasons)
	}
	if ctx := tracker.timeoutContext(time.Now()); ctx != "" {
		t.Fatalf("uncontended worktree task reported a snapshot wait in its timeout context: %q", ctx)
	}
}

// TestInPlaceLockStillWritesDisplayNameReason is the regression guard for the
// in-place path: it must keep its own reason shape, so a worktree snapshot
// hold stays distinguishable from a plain mutex wait.
func TestInPlaceLockStillWritesDisplayNameReason(t *testing.T) {
	t.Parallel()

	const daemonID = "d-mine"
	dir := t.TempDir()
	recorder := newWaitLocalDirectoryRecorder()
	srv := worktreeWaitServer(t, recorder, "running")

	raw, err := json.Marshal(localDirectoryRef{LocalPath: dir, DaemonID: daemonID})
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	resources := []ProjectResourceData{{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw}}
	first := Task{ID: "task-holder", IssueID: "issue-1", ProjectResources: resources}
	second := Task{ID: "task-waiter", IssueID: "issue-2", ProjectResources: resources}

	d := &Daemon{
		cfg:                Config{DaemonID: daemonID},
		client:             NewClient(srv.URL),
		localPathLocks:     NewLocalPathLocker(),
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		cancelPollInterval: time.Hour,
	}
	release, abort := d.acquireLocalDirectoryLockIfNeeded(context.Background(), first, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if abort || release == nil {
		t.Fatalf("first in-place task failed to take the lock (abort=%v)", abort)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rel, _ := d.acquireLocalDirectoryLockIfNeeded(ctx, second, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if rel != nil {
			rel()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for len(recorder.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	release()

	reasons := recorder.all()
	if len(reasons) != 1 {
		t.Fatalf("wait-local-directory calls = %d, want 1: %v", len(reasons), reasons)
	}
	if waitreason.IsWorktreeSnapshot(reasons[0]) {
		t.Fatalf("in-place hold adopted the worktree format: %q", reasons[0])
	}
	if !strings.HasPrefix(reasons[0], filepath.Base(dir)) {
		t.Fatalf("in-place reason %q does not name the directory", reasons[0])
	}
	if strings.Contains(reasons[0], dir) {
		t.Fatalf("in-place reason leaked the absolute path: %q", reasons[0])
	}
}

// TestWorktreeSnapshotTrackerReportsTimeoutContext pins requirement 2: when the
// preparation deadline fires while the task is still queued, the error names
// the holder and the directory so the failure is diagnosable — and carries
// nothing else.
func TestWorktreeSnapshotTrackerReportsTimeoutContext(t *testing.T) {
	t.Parallel()

	tracker := &worktreeSnapshotWaitTracker{}
	start := time.Now()
	tracker.parking(waitreason.WorktreeSnapshot{Holder: "task-holder", LocalDirectory: "NuvioTV"}, start)

	err := prepareTimeoutError(5*time.Minute, tracker, start.Add(5*time.Minute))
	if !errors.Is(err, errTaskPrepareTimeout) {
		t.Fatalf("error %v no longer wraps errTaskPrepareTimeout — handleTask would stop classifying it as a retryable timeout", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "task preparation timed out after 5m0s") {
		t.Fatalf("error = %q, want the existing prefix preserved", msg)
	}
	for _, want := range []string{"worktree snapshot", "NuvioTV", "task-holder", "5m0s"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// TestWorktreeSnapshotTrackerClearsOnAcquire pins the other half of requirement
// 2: once the holder releases and this task owns the lock, a later deadline
// must not blame the snapshot wait.
func TestWorktreeSnapshotTrackerClearsOnAcquire(t *testing.T) {
	t.Parallel()

	tracker := &worktreeSnapshotWaitTracker{}
	start := time.Now()
	tracker.parking(waitreason.WorktreeSnapshot{Holder: "task-holder", LocalDirectory: "NuvioTV"}, start)
	tracker.resumed()

	err := prepareTimeoutError(5*time.Minute, tracker, start.Add(5*time.Minute))
	if !errors.Is(err, errTaskPrepareTimeout) {
		t.Fatalf("error %v no longer wraps errTaskPrepareTimeout", err)
	}
	if got, want := err.Error(), "task preparation timed out after 5m0s"; got != want {
		t.Fatalf("error = %q, want the plain form %q — the task was not waiting on a snapshot", got, want)
	}
}

// TestWorktreeSnapshotTrackerFirstParkWins keeps a second park from rewriting
// the wait start time: the reported wait has to be the one that actually
// elapsed.
func TestWorktreeSnapshotTrackerFirstParkWins(t *testing.T) {
	t.Parallel()

	tracker := &worktreeSnapshotWaitTracker{}
	start := time.Now()
	tracker.parking(waitreason.WorktreeSnapshot{Holder: "holder-a", LocalDirectory: "A"}, start)
	// A second Acquire never happens for one task, so this is a safety net
	// rather than a real sequence — but the reported context must stay the
	// first hold either way.
	tracker.parking(waitreason.WorktreeSnapshot{Holder: "holder-b", LocalDirectory: "B"}, start.Add(time.Minute))

	ctx := tracker.timeoutContext(start.Add(2 * time.Minute))
	if !strings.Contains(ctx, "holder-a") || !strings.Contains(ctx, "A") {
		t.Fatalf("timeout context = %q, want it to describe the first hold", ctx)
	}
	if strings.Contains(ctx, "holder-b") {
		t.Fatalf("timeout context = %q, the second park overwrote the first", ctx)
	}
}

// TestPrepareTimeoutSuffixExcludesHoldWhenAbsent is the no-regression control:
// a task that never waited for a snapshot keeps the byte-identical message the
// server's NormalizeDaemonReason and taskfailure.Classify already key on.
func TestPrepareTimeoutSuffixExcludesHoldWhenAbsent(t *testing.T) {
	t.Parallel()

	var nilTracker *worktreeSnapshotWaitTracker
	if got := nilTracker.timeoutContext(time.Now()); got != "" {
		t.Fatalf("nil tracker returned %q, want empty", got)
	}
	if got := (*worktreeSnapshotWaitTracker)(nil); got != nil {
		t.Fatal("unreachable")
	}
	err := prepareTimeoutError(5*time.Minute, &worktreeSnapshotWaitTracker{}, time.Now())
	if want := "task preparation timed out after 5m0s"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// TestWorktreeSnapshotTrackerIsConcurrencySafe is a cheap -race deliverable for
// the mutex the type carries; the production write and read are on one
// goroutine, but nothing in the type may depend on that.
func TestWorktreeSnapshotTrackerIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	tracker := &worktreeSnapshotWaitTracker{}
	var reads atomic.Int64
	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				_ = tracker.timeoutContext(time.Now())
				reads.Add(1)
			}
		}
	}()
	<-started
	tracker.parking(waitreason.WorktreeSnapshot{Holder: "h", LocalDirectory: "d"}, time.Now())
	tracker.resumed()
	close(stop)
	if reads.Load() == 0 {
		t.Fatal("concurrent reader never ran")
	}
}

// TestRunTaskPrepareTimeoutCarriesSnapshotHolder drives the real runTask path:
// a worktree task parked behind a sibling's path lock whose preparation budget
// expires before the holder releases. The error has to name the holder and the
// directory, while staying the sentinel the server classifies as a retryable
// platform timeout.
//
// This is the composition the unit tests above cannot prove on their own —
// that the tracker installed in runTask is the one its deferred timeout
// handler reads.
func TestRunTaskPrepareTimeoutCarriesSnapshotHolder(t *testing.T) {
	t.Parallel()

	const daemonID = "d-snapshot-timeout"
	dir := t.TempDir()
	recorder := newWaitLocalDirectoryRecorder()
	srv := worktreeWaitServer(t, recorder, "running")

	workspacesRoot := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "claude")
	writeTestExecutable(t, fakeBin, []byte("#!/bin/sh\nexit 0\n"))

	d := &Daemon{
		client:         NewClient(srv.URL),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaces:     make(map[string]*workspaceState),
		runtimeIndex:   map[string]Runtime{"rt-1": {ID: "rt-1", Provider: "claude"}},
		activeEnvRoots: map[string]int{},
		// Leave the holder in place for longer than the budget takes to fire.
		localPathLocks:      NewLocalPathLocker(),
		taskPrepareTimeout:  400 * time.Millisecond,
		prepareLeaseRefresh: 50 * time.Millisecond,
		cancelPollInterval:  time.Hour,
		cfg: Config{
			DaemonID:       daemonID,
			WorkspacesRoot: workspacesRoot,
			Agents:         map[string]AgentEntry{"claude": {Path: fakeBin}},
		},
	}

	task := worktreeTask(t, daemonID, "task-snapshot-timeout", dir)
	task.StartClaimSupported = true
	task.DispatchedAt = "2026-10-31T09:00:00.123456Z"
	task.WorkspaceID = "ws-snapshot-timeout"
	task.RuntimeID = "rt-1"
	task.IssueID = "issue-snapshot-timeout"
	task.AgentID = "agent-snapshot-timeout"
	task.Agent = &AgentData{ID: "agent-snapshot-timeout", Name: "test-agent"}

	assignment, err := localDirectoryAssignmentForTask(task, daemonID)
	if err != nil || assignment == nil {
		t.Fatalf("assignment: %v (nil=%v)", err, assignment == nil)
	}
	release, err := d.localPathLocks.Acquire(context.Background(), assignment.RealPath, "task-holder", nil)
	if err != nil {
		t.Fatalf("preclaim: %v", err)
	}
	t.Cleanup(release)

	_, err = d.runTask(context.Background(), task, "claude", 0,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("runTask returned no error for a preparation blocked behind a holder")
	}
	if !errors.Is(err, errTaskPrepareTimeout) {
		t.Fatalf("error %v lost the prepare-timeout sentinel; the server would stop retrying it", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "worktree snapshot") {
		t.Errorf("error %q does not say what the task was waiting for", msg)
	}
	if !strings.Contains(msg, "task-holder") {
		t.Errorf("error %q does not name the holder", msg)
	}
	if !strings.Contains(msg, filepath.Base(dir)) {
		t.Errorf("error %q does not name the directory", msg)
	}
	if strings.Contains(msg, dir) {
		t.Errorf("error %q carries the absolute directory path", msg)
	}

	// And the row the timeout leaves behind has to be readable by the same
	// parser the server uses, so the two records agree.
	reasons := recorder.all()
	if len(reasons) == 0 {
		t.Fatal("the parked task never wrote a wait reason")
	}
	for _, reason := range reasons {
		if _, ok := waitreason.ParseWorktreeSnapshot(reason); !ok {
			t.Errorf("persisted reason %q is not the documented worktree snapshot shape", reason)
		}
		if strings.Contains(reason, dir) {
			t.Errorf("persisted reason %q carries the absolute directory path", reason)
		}
	}
}
