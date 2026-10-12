# RIC-1575 — multica PR #8764/#8641 downstream merge train (fork-path delivery)

Date: 2026-10-12
Agent: claude-code-agent

## Background
Upstream PRs #8764 (RIC-1024) and #8641 (RIC-951) are fully gate-passed but
cannot be merged on `multica-ai/multica`: account `luyuehm` holds pull-only
permission there (`push:false admin:false maintain:false`), and GitHub rejects
the merge (`luyuehm does not have the correct permissions to execute
MergePullRequest`). Per the RIC-* downstream delivery rule, both changes are
landed onto the `luyuehm/multica` fork main instead.

## Baseline (recorded to disk)
- fork `luyuehm/multica` main before: `76c0211fbcbeff08721f1ffba8cf033ad3733fdc`
- upstream `multica-ai/multica` PR heads:
  - #8764 (RIC-1024) head: `4adb9fcacd5a65a5e98057ab74dddb57d1887dac`
  - #8641 (RIC-951) head: `73193f320d8137f2c2b0b5a69d6e1c14706bb146`
- Local branch SHAs verified identical to PR heads before landing
  (no stale-branch merge).
- Push target: fork only (`origin`/`furtherref` push URLs are `no-push`).

## Landed onto fork main (linear, squash-equivalent cherry-picks)
| PR | Change | Landed commit |
|----|--------|---------------|
| #8764 | RIC-1024 default core-dev tasks to pge-core-squad + run→issue status sync (9 files, +741/-5) | `3293cd1255` |
| #8641 | RIC-951 isolate task compose projects from production stack — guard script + daemon + CI (5 files, +303/-1) | `b6b26e02a6` |

Fork main after: `b6b26e02a6e6c3f292cdd1a1b3d448bd6926e3ed` (pushed).

Both landed commits carry the exact net diff of their PR (stable patch-id
verified for each), and file content on fork main is byte-identical to the PR
heads (diff check on `assignee_default.go`, `guard_compose_project.sh`).

## Verification (local; GH Actions billing disabled → local is authoritative)
- `go build ./...` (server module) ✅ exit 0
- `go test ./internal/service/...` ✅ ok (incl. 11 new RIC-1024 status-sync/default-assignee tests)
- `go test ./internal/handler/...` ✅ ok
- `go test ./internal/daemon/execenv/...` ✅ ok (RIC-1024 touched)
- `go test ./internal/daemon/...` — 7 failures, PROVEN PRE-EXISTING
  environment pollution: identical failures reproduce on clean fork main
  baseline `76c0211f` without these commits; all failing tests
  (`config_test.go`, `identity_test.go`, path-resolution in `daemon_test.go`)
  live in regions untouched by the landed changes and depend on runtime env
  (`OPENCLAW_STATE_DIR`, `MULTICA_TASK_WORKSPACES_ROOT`, …) leaking into the
  test process. RIC-951-touched `TestComposeProjectNameForTask`,
  `TestComposeOpenclawIncludeRoots`, `TestIsBlockedEnvKey`, and the layer-custom
  env test all PASS.
- `bash scripts/guard_compose_project.test.sh` ✅ PASS (RIC-951)
- `go vet` (daemon/service/handler/cmd) ✅ exit 0
- `git diff --check` ✅ clean
- `gitleaks detect --log-opts="--all"`: 66 repo-wide findings, ZERO in files
  landed by this train ✅

## PR closure status
- `multica-ai/multica` #8764, #8641 — closed with landed-on-fork note (merge
  SHA `b6b26e02a6` recorded in comment).

## Output
merged: [#8764, #8641] → fork main `b6b26e02a6`
blocked: []