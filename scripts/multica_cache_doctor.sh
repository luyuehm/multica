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
# git wrapper. Mirrors the daemon's git env for cache access: disable
# terminal prompting (so auth failures are loud, not hangs) and trust all
# directories for ownership (cache dirs may legitimately be owned by a
# different UID). Read-only: `-c safe.directory` is a per-invocation config
# override and writes nothing.
# --------------------------------------------------------------------------
gitx() {
  git -c safe.directory='*' "$@"
}

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
# URL → (host, path), applying the daemon's splitHostAndPath logic
# (repocache/cache.go). Handles URL form (https://host[:port]/path,
# ssh://user@host:port/path), scp-style ([user@]host:path), and bare names /
# absolute paths (empty host). The host is lowercased, matching the daemon's
# strings.ToLower; the path keeps its case, matching the daemon.
# --------------------------------------------------------------------------
__url_to_parts() {
  local raw="$1"
  local host="" path=""

  while [ -n "$raw" ] && [ "${raw: -1}" = "/" ]; do raw="${raw%/}"; done

  # URL form: scheme://[user@]host[:port][/path]
  local re='^[a-zA-Z][a-zA-Z0-9+.-]*://([^/]*)(/.*)?$'
  if [[ "$raw" =~ $re ]]; then
    host="${BASH_REMATCH[1]}"
    path="${BASH_REMATCH[2]:-}"
    path="${path#/}"
    if [[ "$host" == *@* ]]; then host="${host##*@}"; fi
  else
    # scp-style [user@]host:path
    local s="$raw"
    if [[ "$s" == *@* ]]; then s="${s##*@}"; fi
    if [[ "$s" == *:* ]]; then
      host="${s%%:*}"
      path="${s#*:}"
    else
      path="$raw"   # bare name / absolute path → empty host
    fi
  fi

  host="${host,,}"
  printf '%s\t%s' "$host" "$path"
}

# --------------------------------------------------------------------------
# Bare cache dir name → (host, path), the reverse of the daemon's
# bareDirName. The first '+'-separated segment is the (lowercased, %3A-
# decoded) host; every later '+' is a '/' between path segments. A dir name
# with no '+' is the daemon's bare-name fallback (a hostless absolute path
# or bare name), which cannot be reversed unambiguously — returns an empty
# host and path so the caller skips origin verification for it.
# --------------------------------------------------------------------------
__dir_to_parts() {
  local name="$1"
  local body="${name%.git}"
  local host="" path=""

  if [[ "$body" != *+* ]]; then
    printf '\t'
    return 0
  fi
  host="${body%%+*}"
  path="${body#*+}"
  host="${host//%3A/:}"
  host="${host,,}"
  path="${path//+//}"
  printf '%s\t%s' "$host" "$path"
}

# --------------------------------------------------------------------------
# Normalize a path fragment for comparison: strip a trailing .git suffix.
# (Trailing slashes were already removed from the source strings.)
# --------------------------------------------------------------------------
__norm_path() {
  local p="$1"
  p="${p%.git}"
  printf '%s' "$p"
}

# --------------------------------------------------------------------------
# Check a single bare cache directory. Emits zero or more anomaly lines to
# stdout. Returns 1 if any anomaly was found, 0 otherwise. Never mutates.
# --------------------------------------------------------------------------
check_bare_dir() {
  local dir="$1"
  local name="$2"
  local anomalies=0
  local out

  # 1. Must be a bare git repository.
  if ! out="$(gitx -C "$dir" rev-parse --is-bare-repository 2>/dev/null)"; then
    echo "⚠️  $name: not a git repository (or not readable)"
    return 1
  fi
  if [ "$out" != "true" ]; then
    echo "⚠️  $name: not a bare repository (rev-parse reports '$out')"
    return 1
  fi

  # 2. Must have exactly one remote, named origin.
  local remotes
  remotes="$(gitx -C "$dir" remote 2>/dev/null || true)"
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
    # Compare (host, path) pairs scheme-agnostically: the daemon maps https,
    # scp-style, ssh:// and host-port forms of the SAME repo to the SAME dir
    # name, so the doctor must too. Bare-name fallback dirs (no '+') cannot
    # be reversed — skip verification rather than raise a false anomaly.
    local actual expected
    actual="$(gitx -C "$dir" remote get-url origin 2>/dev/null || true)"
    expected="$(__dir_to_parts "$name")"

    local dhost="${expected%%$'\t'*}"
    local dpath="${expected#*$'\t'}"
    if [ -z "$dhost" ] && [ -z "$dpath" ]; then
      # Bare-name / absolute-path fallback: cannot verify the target. Not an
      # anomaly; the single-remote check above is the only signal we have.
      :
    elif [ -n "$actual" ]; then
      local aparts ahost apath
      aparts="$(__url_to_parts "$actual")"
      ahost="${aparts%%$'\t'*}"
      apath="${aparts#*$'\t'}"
      local dnorm anorm
      dnorm="$(__norm_path "$dpath")"
      anorm="$(__norm_path "$apath")"
      if [ "$ahost" != "$dhost" ] || [ "$anorm" != "$dnorm" ]; then
        echo "⚠️  $name: origin points at '$actual', expected to match dir name '$name' (origin mispoint)"
        anomalies=1
      fi
    else
      echo "⚠️  $name: could not read origin URL"
      anomalies=1
    fi
  fi

  return "$anomalies"
}

# --------------------------------------------------------------------------
# Escape a string for JSON. Pure bash — no jq dependency, so the —json mode
# works in minimal runtimes. Handles ", \, \n, \r, \t and strips other
# control bytes.
# --------------------------------------------------------------------------
json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  s="$(printf '%s' "$s" | LC_ALL=C tr -d '[:cntrl:]')"
  printf '"%s"' "$s"
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

  if [ -z "$cache_root" ] || [ ! -e "$cache_root" ]; then
    if $json; then
      printf '{"summary": "no cache root found; check NOT_APPLICABLE", "cache_root": %s, "status": "NOT_APPLICABLE", "repo_count": 0, "anomalies": []}\n' \
        "$(json_escape "${cache_root:-<unresolved>}")"
    else
      echo "SUMMARY: no cache root found — check NOT_APPLICABLE"
      echo "CACHE_ROOT: ${cache_root:-<unresolved>} (does not exist)"
      echo "CACHE_STATUS: NOT_APPLICABLE"
      echo "REPO_COUNT: 0"
      echo "ANOMALIES: (none — nothing to inspect)"
      echo "NEXT_STEP: no bare cache root exists on this machine; nothing to do."
    fi
    return 0
  fi

  if [ ! -d "$cache_root" ] || [ ! -r "$cache_root" ]; then
    if $json; then
      printf '{"summary": "cache root exists but is not readable; check UNKNOWN", "cache_root": %s, "status": "UNKNOWN", "repo_count": 0, "anomalies": []}\n' \
        "$(json_escape "$cache_root")"
    else
      echo "SUMMARY: cache root exists but is not readable — check UNKNOWN"
      echo "CACHE_ROOT: $cache_root"
      echo "CACHE_STATUS: UNKNOWN"
      echo "REPO_COUNT: 0"
      echo "ANOMALIES: (none — root unreadable, cannot inspect)"
      echo "NEXT_STEP: check permissions on the cache root; do not delete or modify caches."
    fi
    return 0
  fi

  local repo_count=0
  local -a anomalies_list=()
  local scan_error=false
  local wsdir child cname rcx
  local ws_children rc

  # The workspace-id directories sit directly under the cache root. Their
  # immediate children are the bare repo dirs (plus daemon-internal dotfiles
  # such as .multica_co_authored_by, which we skip).
  ws_children="$(find "$cache_root" -mindepth 1 -maxdepth 1 -type d 2>/dev/null)"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    scan_error=true
    ws_children=""
  fi

  while IFS= read -r wsdir; do
    [ -z "$wsdir" ] && continue
    local children
    children="$(find "$wsdir" -mindepth 1 -maxdepth 1 2>/dev/null)"
    rc=$?
    if [ "$rc" -ne 0 ]; then
      scan_error=true
      continue
    fi
    while IFS= read -r child; do
      [ -z "$child" ] && continue
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
        anomalies_list+=("$line")
      fi
    done < <(printf '%s\n' "$children" | sort)
  done < <(printf '%s\n' "$ws_children" | sort)

  local summary status
  if [ "$scan_error" = true ] && [ "$repo_count" -eq 0 ]; then
    summary="cache root readable but scan failed; check UNKNOWN"
    status="UNKNOWN"
  elif [ "$repo_count" -eq 0 ]; then
    summary="no bare repos found under $cache_root — check NOT_APPLICABLE"
    status="NOT_APPLICABLE"
  elif [ "$scan_error" = true ]; then
    summary="${#anomalies_list[@]} of $repo_count bare repos have anomalies (and part of the scan failed)"
    status="ANOMALIES"
  elif [ "${#anomalies_list[@]}" -eq 0 ]; then
    summary="all $repo_count bare repos healthy (single origin, correct target)"
    status="PASS"
  else
    summary="${#anomalies_list[@]} of $repo_count bare repos have anomalies"
    status="ANOMALIES"
  fi

  if $json; then
    printf '{"summary": %s, "cache_root": %s, "status": %s, "repo_count": %d, "anomalies": [' \
      "$(json_escape "$summary")" \
      "$(json_escape "$cache_root")" \
      "$(json_escape "$status")" \
      "$repo_count"
    local i=0
    for a in "${anomalies_list[@]+"${anomalies_list[@]}"}"; do
      if [ "$i" -gt 0 ]; then printf ','; fi
      printf '%s' "$(json_escape "$a")"
      i=$((i + 1))
    done
    printf ']}\n'
  else
    echo "SUMMARY: $summary"
    echo "CACHE_ROOT: $cache_root"
    echo "CACHE_STATUS: $status"
    echo "REPO_COUNT: $repo_count"
    if [ "${#anomalies_list[@]}" -gt 0 ]; then
      echo "ANOMALIES:"
      for a in "${anomalies_list[@]}"; do printf '%s\n' "$a"; done
    else
      echo "ANOMALIES: (none)"
    fi
    case "$status" in
      PASS) echo "NEXT_STEP: none — caches are healthy." ;;
      NOT_APPLICABLE) echo "NEXT_STEP: no bare caches exist; nothing to do." ;;
      UNKNOWN) echo "NEXT_STEP: resolve the read/scan error above; do not delete or modify caches." ;;
      *) echo "NEXT_STEP: investigate the ⚠️ lines above; do not delete or modify caches." ;;
    esac
  fi
}

main "$@"