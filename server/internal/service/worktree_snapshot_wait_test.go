package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/waitreason"
)

// These tests pin the DATABASE side of the worktree snapshot hold: what the
// server persists when the daemon parks, and that the text does not outlive the
// hold. The daemon-side shape is covered in internal/daemon; what can only be
// checked here is that the column, the terminal transitions and the response
// mapping agree with it.
//
// They skip when no database is reachable, like the rest of this package.

func snapshotHoldFixture(t *testing.T) (svc *TaskService, pool *pgxpool.Pool, agentID, runtimeID, issueID string) {
	t.Helper()
	pool = newResolveOriginatorPool(t)
	_, _, agentID, issueID = seedAttributionFixture(t, pool)
	if err := pool.QueryRow(context.Background(),
		`SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	svc = &TaskService{Queries: db.New(pool), TxStarter: pool, Bus: events.New()}
	return svc, pool, agentID, runtimeID, issueID
}

func seedDispatchedTask(t *testing.T, pool *pgxpool.Pool, agentID, runtimeID, issueID string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at)
		VALUES ($1, $2, $3, 'dispatched', 0, now())
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&id); err != nil {
		t.Fatalf("insert dispatched task: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, id) })
	return id
}

func taskWaitReason(t *testing.T, pool *pgxpool.Pool, id pgtype.UUID) (string, bool) {
	t.Helper()
	var reason pgtype.Text
	if err := pool.QueryRow(context.Background(),
		`SELECT wait_reason FROM agent_task_queue WHERE id = $1`, id).Scan(&reason); err != nil {
		t.Fatalf("read wait_reason: %v", err)
	}
	return reason.String, reason.Valid
}

// uuidValue parses a uuid column's text form. Fixtures here hand around ids as
// strings, so this is the one place that conversion happens.
func uuidValue(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return id
}

// TestMarkTaskWaitingLocalDirectoryPersistsWorktreeSnapshot exercises the whole
// write path the daemon uses: the daemon's own formatter goes in, the stored
// column comes back parseable, and the response payload carries it.
func TestMarkTaskWaitingLocalDirectoryPersistsWorktreeSnapshot(t *testing.T) {
	svc, pool, agentID, runtimeID, issueID := snapshotHoldFixture(t)
	taskID := seedDispatchedTask(t, pool, agentID, runtimeID, issueID)

	reason := waitreason.WorktreeSnapshot{
		Holder:         "task-holder",
		LocalDirectory: "NuvioTV",
	}.String()
	row, err := svc.MarkTaskWaitingLocalDirectory(context.Background(), taskID, reason)
	if err != nil {
		t.Fatalf("MarkTaskWaitingLocalDirectory: %v", err)
	}
	if row.Status != "waiting_local_directory" {
		t.Fatalf("status = %q, want waiting_local_directory", row.Status)
	}

	stored, ok := taskWaitReason(t, pool, taskID)
	if !ok {
		t.Fatal("wait_reason was not persisted")
	}
	if stored != reason {
		t.Fatalf("stored reason = %q, want %q", stored, reason)
	}
	parsed, ok := waitreason.ParseWorktreeSnapshot(stored)
	if !ok {
		t.Fatalf("stored reason %q does not parse as a worktree snapshot hold", stored)
	}
	if parsed.LocalDirectory != "NuvioTV" || parsed.Holder != "task-holder" {
		t.Fatalf("stored reason lost fields: %+v", parsed)
	}
}

// TestMarkTaskWaitingLocalDirectoryDropsPathBearingReason is the leak guard at
// the database boundary: the server refuses to store a reason that carries an
// absolute path, so even a regressed daemon cannot put the account name in a
// column that reaches every client on the session.
func TestMarkTaskWaitingLocalDirectoryDropsPathBearingReason(t *testing.T) {
	svc, pool, agentID, runtimeID, issueID := snapshotHoldFixture(t)
	taskID := seedDispatchedTask(t, pool, agentID, runtimeID, issueID)

	_, err := svc.MarkTaskWaitingLocalDirectory(context.Background(), taskID,
		"worktree_snapshot_waiting(holder=task-holder, local_directory=/Users/alice/repos/NuvioTV)")
	if err != nil {
		t.Fatalf("MarkTaskWaitingLocalDirectory: %v", err)
	}
	if stored, ok := taskWaitReason(t, pool, taskID); ok {
		t.Fatalf("absolute path was persisted in wait_reason: %q", stored)
	}
}

// TestStartAgentTaskClearsWorktreeSnapshotReason covers the "holder released,
// task can start" half of the contract: once the daemon owns the lock and
// starts the run, the hold text is gone, so no reader can mistake a running
// task for a parked one.
func TestStartAgentTaskClearsWorktreeSnapshotReason(t *testing.T) {
	svc, pool, agentID, runtimeID, issueID := snapshotHoldFixture(t)
	taskID := seedDispatchedTask(t, pool, agentID, runtimeID, issueID)

	if _, err := svc.MarkTaskWaitingLocalDirectory(context.Background(), taskID,
		waitreason.WorktreeSnapshot{Holder: "task-holder", LocalDirectory: "NuvioTV"}.String()); err != nil {
		t.Fatalf("mark waiting: %v", err)
	}

	var dispatchedAt pgtype.Timestamptz
	if err := pool.QueryRow(context.Background(),
		`SELECT dispatched_at FROM agent_task_queue WHERE id = $1`, taskID).Scan(&dispatchedAt); err != nil {
		t.Fatalf("read dispatched_at: %v", err)
	}
	started, err := svc.StartTaskForClaim(context.Background(), db.LockAgentTaskStartClaimParams{
		ID:           taskID,
		RuntimeID:    uuidValue(t, runtimeID),
		DispatchedAt: dispatchedAt,
	})
	if err != nil {
		t.Fatalf("StartTaskForClaim: %v", err)
	}
	if started.Status != "running" {
		t.Fatalf("status = %q, want running", started.Status)
	}
	if stored, ok := taskWaitReason(t, pool, taskID); ok {
		t.Fatalf("wait_reason survived the start transition: %q", stored)
	}
}

// TestFailAgentTaskClearsWorktreeSnapshotReason covers the path this change
// added. A preparation that times out behind a holder fails the task while it
// is still in waiting_local_directory; leaving the hold text on the failed row
// would describe a wait that is over.
func TestFailAgentTaskClearsWorktreeSnapshotReason(t *testing.T) {
	svc, pool, agentID, runtimeID, issueID := snapshotHoldFixture(t)
	taskID := seedDispatchedTask(t, pool, agentID, runtimeID, issueID)

	if _, err := svc.MarkTaskWaitingLocalDirectory(context.Background(), taskID,
		waitreason.WorktreeSnapshot{Holder: "task-holder", LocalDirectory: "NuvioTV"}.String()); err != nil {
		t.Fatalf("mark waiting: %v", err)
	}
	if _, err := svc.FailTask(context.Background(), taskID,
		"task preparation timed out after 5m0s while waiting for a consistent worktree snapshot of NuvioTV (holder task task-holder, waited 5m0s)",
		"", "", "", "timeout", false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	if stored, ok := taskWaitReason(t, pool, taskID); ok {
		t.Fatalf("wait_reason survived the fail transition: %q", stored)
	}
}
