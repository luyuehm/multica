# RIC-1519 — furtherref/multica 8-PR downstream merge train (fork-path delivery)

Date: 2026-10-10
Agent: claude-code-agent

## Baseline (recorded to disk)
- fork `luyuehm/multica` main SHA: `374632913f73d9ac74265be389c9402d92635cc1`
- upstream `furtherref/multica` main SHA: `6f3c631c6877ef023ef82cb0ab79bda9fd739256`
- Both fetched before work; push only to fork (`furtherref` push URL = `no-push`).

## Pre-existing state discovered
PR #113 / #114 / #115 content was already landed in fork main by the earlier
RIC-1359 merge commit `481dbc6033` (the PR #118 delivery tree, RIC-903/905/906).
PR #118 (which supersedes #113/#114/#115) was therefore not re-landed — its
content is already on fork main.

## Landed onto fork main in this task (4 merge commits on `ric-1519-merge-train`)
| PR | Head | Merged commit | Notes |
|----|------|---------------|-------|
| #112 | 6fd250a81f | b9a045c12b | test-only; fake agent CLIs via writeTestExecutable |
| #109 | fcd86687d0 | c6731e1472 | bare-cache doctor script + CI; bash-3.2 portability fix |
| #108 | 3257700a3c | e36712ada6 | RIC-806 quarantine gate; migration 400→405 renumber |
| #117 | fd79001e14 | 4d65030279 | RIC-904 management UI; migrations 545/546→910/911 renumber |

## Conflict resolution (semantic, no side-taken blindly)
- #108 `agent.sql`: kept fork's `GetCommentThreadRootID` + PR's health queries.
- #108 migration `400_agent_health_state` → `405` (400 taken by
  plugin_hook_schedule_installation_key_index on fork).
- #117 migrations `545/546` → `910/911` (taken on fork); sqlc regen; picker
  summary dropped ArchivedAt to match enabled/position columns; tests updated
  to pass issue_title to the fork's create helper.
- #117 also registered the missing `907_issue_template_workspace_name_active_index`
  concurrent-index cleanup in cmd/migrate (pre-existing RIC-1359 gap) — the
  `TestEveryConcurrentUpBuildHasCleanup` suite now passes.
- #109 ported CI wiring to the fork's ci-paths.json/script-checks structure.

## Verification (local; GH Actions disabled signal not relied on)
- `go build ./...` ✅ `go vet` (service/handler/migrate/agent) ✅
- `go test ./internal/migrations/` ✅ `go test ./cmd/migrate/` ✅
- `go test ./pkg/agent/` (full, incl. PR #112 guard) ✅
- `go test ./internal/service/` ✅ (agent_health gate)
- `go test ./internal/handler/` DB-backed ✅ (fresh pg17 + all migrations;
  3 autopilot body-template failures are PRE-EXISTING on fork main, files
  untouched by this train — expected-200-vs-201 from RIC-947, not from here)
- `bash scripts/multica_cache_doctor.test.sh` ✅ (9 groups, macOS bash 3.2)
- `node --test scripts/ci-scope.test.mjs` ✅ (39/39)
- `make sqlc` regenerated; generated files consistent ✅
- `git diff --check` ✅
- `gitleaks detect --no-git -v`: 39 findings, ALL pre-existing test-fixture /
  `.env.bak` tokens; ZERO in files changed by this train ✅

## Output
merged: [#112, #109, #108, #117]
already-in-fork-main (superseded): #113, #114, #115, #118 (RIC-1359 landed #118 tree)
blocked: []

## PR closure status (furtherref/multica, 2026-10-10)
- CLOSED with superseded-by #118 note: #115, #114, #113
- CLOSED with landed-on-fork note (merge SHA in comment): #109, #108, #117, #118
- OPEN (no close permission — authored by furtherref, not luyuehm): #112;
  landed-on-fork note posted as a comment

## Verification commands recorded
- `git -C <fork> log --oneline -5 origin/main` → 9a2f406a55..374632913f
- `gitleaks detect --no-git -v` → 39 findings, 0 in train-changed files
- `gh api repos/luyuehm/multica/commits/main --jq .sha` → 9a2f406a55631d3e3b11d6593205bdec926219f2
