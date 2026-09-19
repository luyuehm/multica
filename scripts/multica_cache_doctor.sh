#!/usr/bin/env bash
# ==========================================================================
# multica_cache_doctor.sh — read-only health check for Multica bare-repo caches
#
# The daemon clones workspace repositories as bare git repos under
#   $MULTICA_TASK_WORKSPACES_ROOT/.repos/<workspace-id>/<baredir>.git
# (see server/internal/daemon/repocache/cache.go). A cache is healthy when it
# is a bare repo whose only remote is `origin` and whose origin URL points at
# the repository its directory name advertises. Anything else risks polluting
# later worktrees (multi-remote mixing, origin mispointed at a non-primary
# repo, a bare repo whose directory name lied about what it holds).
#
# This script is STRICTLY READ-ONLY. It never modifies, deletes, or fetches
# caches, and never writes to any git config. Its exit code is always 0 for
# the shell even when anomalies are found — the caller inspects the structured
# output instead.
#
# Output contract (consumed by the AutoOps autopilot):
#   SUMMARY:       one-line verdict
#   CACHE_ROOT:    the cache root inspected (or why none was found)
#   CACHE_STATUS:  PASS | ANOMALIES | NOT_APPLICABLE | UNKNOWN
#   REPO_COUNT:    number of bare repo directories examined
#   ANOMALIES:     zero or more ⚠️ lines describing problems
#   NEXT_STEP:     remediation hint
#
# Usage:
#   bash scripts/multica_cache_doctor.sh [--root <path>] [--json]
#   --root  inspect a specific cache root instead of the resolved default
#   --json  emit a machine-readable JSON summary instead of key:value lines
# ==========================================================================
set -euo pipefail

# --------------------------------------------------------------------------
# Resolve the cache root. The daemon records the real workspaces root in
# MULTICA_TASK_WORKSPACES_ROOT; fall back to the documented default. We
# deliberately do NOT guess ~/.multica*/*/.repos — that pattern has never
# matched the daemon layout and produced false "no caches" results before.
# --------------------------------------------------------------------------
resolve_cache_root() {
  local root="${1:-}"
  if [ -n "$root" ]; then
    printf '%s' "$root"
    return 0
  fi
  if [ -n "${MULTICA_TASK_WORKSPACES_ROOT:-}" ]; then
    printf '%s/.repos' "$MULTICA_TASK_WORKSPACES_ROOT"
    return 0
  fi
  if [ -n "${MULTICA_WORKSPACES_ROOT:-}" ]; then
    printf '%s/.repos' "$MULTICA_WORKSPACES_ROOT"
    return 0
  fi
  printf '%s/multica_workspaces/.repos' "${HOME:-}"
}

# --------------------------------------------------------------------------
# Reverse of the daemon's bareDirName: given a bare cache directory name such
# as github.com+org+repo.git, reconstruct the canonical origin URL it SHOULD
# point at. Used to detect origin mispoint without trusting the remote.
# --------------------------------------------------------------------------
dir_name_to_expected_url() {
  local name="$1"
  local body="${name%.git}"
  # Host plus path segments joined by '+'; a %3A in the host is a ':' port.
  # The first '+' separates host from path; every later '+' is a '/' between
  # path segments (GitHub/GitLab forbid '+' in path segments, so this is lossless).
  local host="${body%%+*}"
  local rest="${body#*+}"
  if [ "$rest" = "$body" ]; then
    # No '+': bare-name fallback (daemon falls back to a bare repo name).
    printf 'file://%s' "$name"
    return 0
  fi
  host="${host//%3A/:}"
  rest="${rest//+//}"
  printf 'https://%s/%s' "$host" "$rest"
}

# --------------------------------------------------------------------------
# Normalize a URL for comparison: strip trailing slashes, drop an optional
# .git suffix so https://host/a/b.git and https://host/a/b compare equal.
# --------------------------------------------------------------------------
normalize_url() {
  local u="$1"
  u="${u%/}"
  u="${u%.git}"
  printf '%s' "$u"
}

# --------------------------------------------------------------------------
# Check a single bare cache directory. Emits anomaly lines to stdout; returns
# 1 if any anomaly was found, 0 otherwise. Never mutates anything.
# --------------------------------------------------------------------------
check_bare_dir() {
  local dir="$1"
  local name="$2"
  local anomalies=0
  local out

  # 1. Must be a bare git repository.
  if ! out="$(git -C "$dir" rev-parse --is-bare-repository 2>/dev/null)"; then
    echo "⚠️  $name: not a git repository"
    return 1
  fi
  if [ "$out" != "true" ]; then
    echo "⚠️  $name: not a bare repository (rev-parse reports '$out')"
    return 1
  fi

  # 2. Must have exactly one remote, named origin.
  local remotes
  remotes="$(git -C "$dir" remote 2>/dev/null || true)"
  if [ -z "$remotes" ]; then
    echo "⚠️  $name: bare repo has no remotes"
    anomalies=1
  elif [ "$(printf '%s\n' "$remotes" | wc -l | tr -d ' ')" -ne 1 ] || [ "$remotes" != "origin" ]; then
    local joined
    joined="$(printf '%s\n' "$remotes" | tr '\n' ',')"
    echo "⚠️  $name: expected exactly one remote 'origin', found: ${joined%,}"
    anomalies=1
  else
    # 3. Origin must point at the repository the dir name advertises.
    local actual expected
    actual="$(git -C "$dir" remote get-url origin 2>/dev/null || true)"
    expected="$(dir_name_to_expected_url "$name")"
    if [ -n "$actual" ] && [ -n "$expected" ]; then
      if [ "$(normalize_url "$actual")" != "$(normalize_url "$expected")" ]; then
        echo "⚠️  $name: origin points at '$actual', expected '$expected' (origin mispoint)"
        anomalies=1
      fi
    else
      echo "⚠️  $name: could not read origin URL (actual='$actual')"
      anomalies=1
    fi
  fi

  return "$anomalies"
}

main() {
  local root=""
  local json=false

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --root) root="${2:-}"; shift 2 ;;
      --json) json=true; shift ;;
      *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
  done

  local cache_root
  cache_root="$(resolve_cache_root "$root")"

  if [ -z "$cache_root" ] || [ ! -d "$cache_root" ]; then
    if $json; then
      cat <<EOF
{"summary": "no cache root found; check NOT_APPLICABLE", "cache_root": "$cache_root", "status": "NOT_APPLICABLE", "repo_count": 0, "anomalies": []}
EOF
    else
      echo "SUMMARY: no cache root found — check NOT_APPLICABLE"
      echo "CACHE_ROOT: ${cache_root:-<unresolved>} (directory does not exist)"
      echo "CACHE_STATUS: NOT_APPLICABLE"
      echo "REPO_COUNT: 0"
      echo "ANOMALIES: (none — nothing to inspect)"
      echo "NEXT_STEP: no bare cache root exists on this machine; nothing to do."
    fi
    return 0
  fi

  local repo_count=0 anomalies=0
  local line anomalies_out summary
  anomalies_out=""

  # The workspace-id directories sit directly under the cache root. Their
  # immediate children are the bare repo dirs (plus daemon-internal dotfiles
  # such as .multica_co_authored_by, which we skip).
  while IFS= read -r wsdir; do
    [ -z "$wsdir" ] && continue
    while IFS= read -r child; do
      [ -z "$child" ] && continue
      local cname
      cname="$(basename "$child")"
      case "$cname" in
        .*) continue ;;
        *.git) ;;
        *) continue ;;
      esac
      repo_count=$((repo_count + 1))
      if line="$(check_bare_dir "$child" "$cname")"; then
        :
      else
        anomalies=$((anomalies + 1))
        anomalies_out="${anomalies_out}${line}\n"
      fi
    done < <(find "$wsdir" -mindepth 1 -maxdepth 1 2>/dev/null | sort)
  done < <(find "$cache_root" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort)

  if [ "$repo_count" -eq 0 ]; then
    summary="no bare repos found under $cache_root — check NOT_APPLICABLE"
    status="NOT_APPLICABLE"
  elif [ "$anomalies" -eq 0 ]; then
    summary="all $repo_count bare repos healthy (single origin, correct target)"
    status="PASS"
  else
    summary="$anomalies of $repo_count bare repos have anomalies"
    status="ANOMALIES"
  fi

  if $json; then
    printf '{"summary": %s, "cache_root": %s, "status": %s, "repo_count": %d, "anomalies": [%s]}\n' \
      "$(printf '%s' "$summary" | jq -R -s . 2>/dev/null || printf '"%s"' "$summary")" \
      "$(printf '%s' "$cache_root" | jq -R -s . 2>/dev/null || printf '"%s"' "$cache_root")" \
      "$(printf '%s' "$status" | jq -R -s . 2>/dev/null || printf '"%s"' "$status")" \
      "$repo_count" \
      "$(printf '%b' "$anomalies_out" | sed '/^$/d' | sed 's/^/"/; s/$/",/' | tr -d '\n' | sed 's/,$//')"
  else
    echo "SUMMARY: $summary"
    echo "CACHE_ROOT: $cache_root"
    echo "CACHE_STATUS: $status"
    echo "REPO_COUNT: $repo_count"
    if [ -n "$anomalies_out" ]; then
      echo "ANOMALIES:"
      printf '%b' "$anomalies_out" | sed '/^$/d'
    else
      echo "ANOMALIES: (none)"
    fi
    if [ "$status" = "PASS" ]; then
      echo "NEXT_STEP: none — caches are healthy."
    elif [ "$status" = "NOT_APPLICABLE" ]; then
      echo "NEXT_STEP: no bare caches exist; nothing to do."
    else
      echo "NEXT_STEP: investigate the ⚠️ lines above; do not delete or modify caches."
    fi
  fi
}

main "$@"
