## What does this PR do?

Persist a structured, low-sensitivity `wait_reason` when a worktree task parks
waiting for a consistent snapshot of a shared local directory, and carry that
holder context into the preparation timeout error — so the board, failover and
lock-heal code can finally tell a *worktree snapshot hold* apart from a
traditional in-place directory lock.

Previously, once a task entered worktree mode it logged `worktree snapshot
waiting for holder` but wrote nothing into
`agent_task_queue.wait_reason`, and a wait past the hardcoded 5m surfaced only
as `task preparation timed out after 5m0s`. Two distinct operational holds now
share that column, and this gives the worktree one a fixed, parseable shape.

## Related Issue

RIC-2138 — [Bug] 持久化 worktree 快照等待原因并保留 holder 超时上下文

## Type of Change

- [x] Bug fix (non-breaking change that fixes an issue)
- [x] Tests (adding or improving test coverage)

## Changes Made

- `server/pkg/waitreason/` (new) — the wire format, shared by daemon (writer)
  and server (reader): `worktree_snapshot_waiting(holder=<task-id>,
  local_directory=<display-name>)`. `local_directory` is the display name,
  never an absolute path.
- `server/internal/daemon/worktree_snapshot_wait.go` (new) + `daemon.go` —
  persist the reason when the hold begins, clear it on acquire, and append a
  redacted holder summary to the prepare-timeout error.
- `server/internal/service/task.go` — extend `sanitizeWaitReason` to drop a
  snapshot-shaped reason that carries a path.
- `server/pkg/db/queries/agent.sql` + `generated/agent.sql.go` —
  `FailAgentTask` now NULLs `wait_reason` (a preparation that timed out behind
  a holder must not leave the hold text on a failed row).
- `packages/core/chat/wait-reason.ts` (new) + `task-status-pill.tsx` + 5 locale
  files — render the hold as a sentence ("waiting for a consistent snapshot of
  NuvioTV") instead of dumping `holder=` / `local_directory=` to the user.
- `server/internal/handler/chat.go`, `packages/core/chat/pending.ts` — correct
  the now-stale "wait_reason is never cleared" comments.

No new/changed HTTP route or request field; no write-API surface added.

## How to Test

Local verification (recorded on the issue):

1. `go build ./...` — passes.
2. `go test ./pkg/waitreason/ ./internal/daemon/` (snapshot/timeout/lock cases)
   — all ok; the 6 worktree-snapshot daemon tests pass including
   `TestRunTaskPrepareTimeoutCarriesSnapshotHolder` and concurrency safety.
3. DB-backed cases against a throwaway Postgres 17 + full migrations:
   persistence, path-drop, start-clear, fail-clear all pass; the fail path
   logs `...timed out after 5m0s while waiting for a consistent worktree
   snapshot of NuvioTV (holder task task-holder, waited 5m0s)`.
4. `pnpm typecheck` 10/10; `pnpm lint` 0 errors; `packages/views` full suite
   (447 files / 5514 tests) green.
5. `make sqlc` idempotent (no diff on re-run).

An independent reviewer (RIC-2593) re-ran the suite in a fresh worktree and
confirmed the code passes; the only open gap was that the work had not yet
been pushed — this PR closes that.

## Checklist

- [x] I have run tests locally and they pass
- [x] I have added or updated tests where applicable
- [x] I have considered and documented any risks above

## AI Disclosure

**AI tool used:** Multica agent (claude-code-agent) + Claude Code

**Prompt / approach:** Issue-driven bug fix. The daemon and server share one
wire-format package so the two ends cannot drift; the status pill unwraps the
machine-readable envelope into a human sentence. Independent review agent
re-verified the tests in an isolated worktree.

---

**Baseline note:** developed on the stable tag line per the issue's
"仅稳定 tag 基线开发" requirement (baseline `v0.5.3`). This PR targets the fork's
stable line (`upgrade/v0.5.3`), which yields the clean 22-file diff above.
`main` is a divergent RIC-downstream trunk and would surface ~300 unrelated
upstream commits, so it is not the reviewable base. A few files overlap with
RIC-1281 on that base, so a short rebase onto the base tip is expected before
merge. Retarget to `main` (or a different base) in one click if that is where
this is meant to land.
