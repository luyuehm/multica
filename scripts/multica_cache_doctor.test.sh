#!/usr/bin/env bash
# ==========================================================================
# Test harness for scripts/multica_cache_doctor.sh
#
# Covers the four scenarios the doctor must distinguish without lying:
#   1. normal    — a healthy bare repo (single origin, correct target) → PASS
#   2. missing   — no cache root at all → NOT_APPLICABLE (never a false PASS)
#   3. wrong remote — origin mispointed / multi-remote → ANOMALIES
#   4. empty cache — root exists but holds no bare repos → NOT_APPLICABLE
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
  git init -q --bare "$dir"
  git -C "$dir" remote add origin "$url"
}

# --- 1. normal: healthy bare repo → PASS ---------------------------------
root1="$tmp/normal/.repos/ws-1"
mkdir -p "$root1"
init_bare "$root1/github.com+org+alpha.git" "https://github.com/org/alpha.git"
init_bare "$root1/github.com+org+beta.git" "https://github.com/org/beta"
track "$root1"

out1="$(MULTICA_TASK_WORKSPACES_ROOT="$tmp/normal" "$doctor" 2>&1 || true)"
case "$out1" in
  *"CACHE_STATUS: PASS"*) ;;
  *) fail "normal scenario: expected PASS, got:$out1" ;;
esac
if [ "$(printf '%s' "$out1" | grep -c '^⚠️')" -ne 0 ]; then
  fail "normal scenario: unexpected anomalies:$out1"
fi

# --- 2. missing: no cache root → NOT_APPLICABLE ---------------------------
out2="$(MULTICA_TASK_WORKSPACES_ROOT="$tmp/does-not-exist" "$doctor" 2>&1 || true)"
case "$out2" in
  *"CACHE_STATUS: NOT_APPLICABLE"*) ;;
  *) fail "missing scenario: expected NOT_APPLICABLE, got:$out2" ;;
esac

# --- 3. wrong remote: origin mispoint → ANOMALIES -------------------------
root3="$tmp/wrong/.repos/ws-1"
mkdir -p "$root3"
init_bare "$root3/github.com+org+alpha.git" "https://github.com/other/pwned.git"
init_bare "$root3/github.com+org+multi.git" "https://github.com/org/multi.git"
git -C "$root3/github.com+org+multi.git" remote add upstream "https://github.com/elsewhere/upstream.git"
track "$root3"

out3="$(MULTICA_TASK_WORKSPACES_ROOT="$tmp/wrong" "$doctor" 2>&1 || true)"
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

# --- 4. empty cache: root exists, no repos → NOT_APPLICABLE ---------------
root4="$tmp/empty/.repos/ws-1"
mkdir -p "$root4"
track "$root4"

out4="$(MULTICA_TASK_WORKSPACES_ROOT="$tmp/empty" "$doctor" 2>&1 || true)"
case "$out4" in
  *"CACHE_STATUS: NOT_APPLICABLE"*) ;;
  *) fail "empty-cache scenario: expected NOT_APPLICABLE, got:$out4" ;;
esac

echo "PASS: all 4 cache-doctor scenarios behaved correctly"
