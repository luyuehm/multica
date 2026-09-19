#!/usr/bin/env bash
# ==========================================================================
# Test harness for scripts/multica_cache_doctor.sh
#
# Covers the scenarios the doctor must distinguish without lying:
#   1. normal     — a healthy bare repo (single origin, correct target) → PASS
#   2. missing    — no cache root at all → NOT_APPLICABLE (never a false PASS)
#   3. wrong remote — origin mispointed / multi-remote / no remote → ANOMALIES
#   4. empty cache — root exists but holds no bare repos → NOT_APPLICABLE
#   5. scheme family — healthy repos cloned from ssh/scp/http origins that
#      the daemon maps to the SAME dir name must NOT be flagged → PASS
#   6. bare-name fallback — a dir with no '+' cannot be reverse-mapped; must
#      not raise a false anomaly → PASS (as long as single origin)
#   7. unreadable root — root exists but is a file / not readable → UNKNOWN
#   8. JSON validity — --json must parse even with no jq and odd paths
#
# Runs entirely in a temp directory; never touches the real daemon cache.
# ==========================================================================
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
doctor="$root_dir/scripts/multica_cache_doctor.sh"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/multica-cache-doctor-test.XXXXXX")"
scratch=()

cleanup() {
  rm -rf "$tmp" "${scratch[@]+"${scratch[@]}"}"
}
trap cleanup EXIT

track() { scratch+=("$1"); }

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# init_bare <dir> <origin-url> creates a real bare repo with the given origin.
init_bare() {
  local dir="$1" url="$2"
  mkdir -p "$(dirname "$dir")"
  git init -q --bare "$dir"
  if [ -n "$url" ]; then
    git -C "$dir" remote add origin "$url"
  fi
}

# run_doctor <workspaces-root> [extra args...] → runs the doctor and captures stdout.
run_doctor() {
  local root="$1"; shift
  MULTICA_TASK_WORKSPACES_ROOT="$root" "$doctor" "$@"
}

# --- 1. normal: healthy bare repo → PASS ---------------------------------
root1="$tmp/normal"
init_bare "$root1/.repos/ws-1/github.com+org+alpha.git" "https://github.com/org/alpha.git"
init_bare "$root1/.repos/ws-1/github.com+org+beta.git" "https://github.com/org/beta"
track "$root1"

out1="$(run_doctor "$root1" 2>&1 || true)"
case "$out1" in
  *"CACHE_STATUS: PASS"*) ;;
  *) fail "normal scenario: expected PASS, got:$out1" ;;
esac
if [ "$(printf '%s' "$out1" | grep -c '^⚠️')" -ne 0 ]; then
  fail "normal scenario: unexpected anomalies:$out1"
fi

# --- 2. missing: no cache root → NOT_APPLICABLE ---------------------------
out2="$(run_doctor "$tmp/does-not-exist" 2>&1 || true)"
case "$out2" in
  *"CACHE_STATUS: NOT_APPLICABLE"*) ;;
  *) fail "missing scenario: expected NOT_APPLICABLE, got:$out2" ;;
esac

# --- 3. wrong remote: origin mispoint / multi-remote / no remote → ANOMALIES
root3="$tmp/wrong"
init_bare "$root3/.repos/ws-1/github.com+org+alpha.git" "https://github.com/other/pwned.git"
init_bare "$root3/.repos/ws-1/github.com+org+multi.git" "https://github.com/org/multi.git"
git -C "$root3/.repos/ws-1/github.com+org+multi.git" remote add upstream "https://github.com/elsewhere/upstream.git"
init_bare "$root3/.repos/ws-1/github.com+org+empty.git" ""
track "$root3"

out3="$(run_doctor "$root3" 2>&1 || true)"
case "$out3" in
  *"CACHE_STATUS: ANOMALIES"*) ;;
  *) fail "wrong-remote scenario: expected ANOMALIES, got:$out3" ;;
esac
case "$out3" in
  *"origin mispoint"*) ;;
  *) fail "wrong-remote scenario: expected origin-mispoint anomaly:$out3" ;;
esac
case "$out3" in
  *"expected exactly one remote 'origin', found: origin,upstream"*) ;;
  *) fail "wrong-remote scenario: expected multi-remote anomaly:$out3" ;;
esac
case "$out3" in
  *"bare repo has no remotes"*) ;;
  *) fail "wrong-remote scenario: expected no-remote anomaly:$out3" ;;
esac

# --- 4. empty cache: root exists, no repos → NOT_APPLICABLE ---------------
root4="$tmp/empty"
mkdir -p "$root4/.repos/ws-1"
track "$root4"

out4="$(run_doctor "$root4" 2>&1 || true)"
case "$out4" in
  *"CACHE_STATUS: NOT_APPLICABLE"*) ;;
  *) fail "empty-cache scenario: expected NOT_APPLICABLE, got:$out4" ;;
esac

# --- 5. scheme family: ssh/scp/http origins on healthy repos → PASS -------
root5="$tmp/schemes"
init_bare "$root5/.repos/ws-1/github.com+org+my-repo.git" "git@github.com:org/my-repo.git"
init_bare "$root5/.repos/ws-1/gitlab.example.com%3A22+group+sub+repo.git" "ssh://git@gitlab.example.com:22/group/sub/repo.git"
init_bare "$root5/.repos/ws-1/github.com+other+http.git" "http://github.com/other/http.git"
track "$root5"

out5="$(run_doctor "$root5" 2>&1 || true)"
case "$out5" in
  *"CACHE_STATUS: PASS"*) ;;
  *) fail "scheme-family scenario: expected PASS, got:$out5" ;;
esac
if [ "$(printf '%s' "$out5" | grep -c '^⚠️')" -ne 0 ]; then
  fail "scheme-family scenario: unexpected anomalies:$out5"
fi

# --- 6. bare-name fallback: no '+' in dir name, single origin → PASS ------
root6="$tmp/barename"
init_bare "$root6/.repos/ws-1/some-repo.git" "https://github.com/org/some-repo.git"
track "$root6"

out6="$(run_doctor "$root6" 2>&1 || true)"
case "$out6" in
  *"CACHE_STATUS: PASS"*) ;;
  *) fail "bare-name scenario: expected PASS, got:$out6" ;;
esac

# --- 7. unreadable root: cache root is a file → UNKNOWN -------------------
root7="$tmp/unreadable"
mkdir -p "$root7"
printf 'not a directory\n' > "$root7/.repos"
track "$root7"

out7="$(run_doctor "$root7" 2>&1 || true)"
case "$out7" in
  *"CACHE_STATUS: UNKNOWN"*) ;;
  *) fail "unreadable-root scenario: expected UNKNOWN, got:$out7" ;;
esac

# --- 8.5 secret redaction: a credential-bearing origin must never leak ----
root85="$tmp/leak"
init_bare "$root85/.repos/ws-1/github.com+org+alpha.git" "https://secret-token-abc123@github.com/other/pwned.git"
track "$root85"

out85="$(run_doctor "$root85" 2>&1 || true)"
case "$out85" in
  *"secret-token-abc123"*)
    fail "redaction scenario: credential leaked into output:$out85" ;;
  *"origin mispoint"*) ;;
  *) fail "redaction scenario: expected an origin-mispoint anomaly:$out85" ;;
esac

# --- 8. JSON validity: parses with no jq on PATH and odd cache paths ------
root8="$tmp/jsontest"
init_bare "$root8/.repos/ws-1/github.com+org+alpha.git" "https://github.com/org/beta.git"  # mispoint → anomaly path
track "$root8"

# Strip jq from PATH (use a bare env with only /usr/bin and /bin). Bash
# functions do not survive `env -i`, so invoke bash in the stripped env and
# pass the workspaces root through env explicitly.
json8="$(env -i PATH="/usr/bin:/bin" HOME="$HOME" MULTICA_TASK_WORKSPACES_ROOT="$root8" bash "$doctor" --json 2>&1 || true)"
if ! printf '%s' "$json8" | python3 -m json.tool >/dev/null 2>&1; then
  fail "json scenario: output did not parse:$json8"
fi
case "$json8" in
  *'"status": "ANOMALIES"'*) ;;
  *) fail "json scenario: expected ANOMALIES status in JSON:$json8" ;;
esac

# no-cache-root JSON with a quote and backtick in the path
json8b="$(env -i PATH="/usr/bin:/bin" HOME="$HOME" MULTICA_TASK_WORKSPACES_ROOT="$root8/nq\"\`t" bash "$doctor" --json 2>&1 || true)"
if ! printf '%s' "$json8b" | python3 -m json.tool >/dev/null 2>&1; then
  fail "json scenario (odd path): output did not parse:$json8b"
fi

echo "PASS: all 9 cache-doctor scenario groups behaved correctly"
