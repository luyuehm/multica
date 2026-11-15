package waitreason

import (
	"strings"
	"testing"
)

// TestWorktreeSnapshotRoundTrip pins the wire format. The daemon writes it and
// the server parses it to decide whether a hold is a worktree snapshot, so the
// literal shape is a contract between them, not an implementation detail.
func TestWorktreeSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	got := WorktreeSnapshot{Holder: "task-holder", LocalDirectory: "NuvioTV"}.String()
	const want = "worktree_snapshot_waiting(holder=task-holder, local_directory=NuvioTV)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	parsed, ok := ParseWorktreeSnapshot(got)
	if !ok {
		t.Fatalf("ParseWorktreeSnapshot(%q) rejected the format it produced", got)
	}
	if parsed.Holder != "task-holder" || parsed.LocalDirectory != "NuvioTV" {
		t.Fatalf("round trip lost fields: %+v", parsed)
	}
}

// TestWorktreeSnapshotValuesStaySingleLine covers the characters that would
// otherwise break either the envelope or a one-line UI label. A directory name
// is user-supplied, so none of this can be assumed away.
func TestWorktreeSnapshotValuesStaySingleLine(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain":                 "plain",
		"with, comma":           "with- comma",
		"(parens)":              "-parens-",
		"key=value":             "key-value",
		"line\nbreak":           "line break",
		"  padded  ":            "padded",
		"":                      "-",
		"-----":                 "-----",
		"(=)":                   "---",
		"\\path\\like\\name":    "\\path\\like\\name",
		"user@example.com/repo": "user@example.com/repo",
		"名前-ノート":                "名前-ノート",
	}
	for in, want := range cases {
		if got := Value(in); got != want {
			t.Errorf("Value(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWorktreeSnapshotValueIsCapped keeps a pathological name from dominating
// the column and the status pill, while never splitting a rune.
func TestWorktreeSnapshotValueIsCapped(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("界", MaxValueLen)
	got := Value(long)
	if len(got) > MaxValueLen {
		t.Fatalf("Value length = %d bytes, want <= %d", len(got), MaxValueLen)
	}
	if !strings.HasPrefix(long, got) {
		t.Fatalf("Value truncated mid-rune or reordered: %q", got)
	}
	if got := (WorktreeSnapshot{LocalDirectory: long}).String(); !strings.HasSuffix(got, ")") {
		t.Fatalf("a capped value broke the envelope: %q", got)
	}
}

// TestParseWorktreeSnapshotRejectsOtherShapes is the classification guard. The
// in-place format lands in the same column, and treating it as a snapshot wait
// is exactly the confusion this format exists to prevent.
func TestParseWorktreeSnapshotRejectsOtherShapes(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{
		"",
		"NuvioTV (held by task a1b2c3d4)",
		"local_directory /Users/me/repo (held by task a1b2c3d4)",
		"worktree_snapshot_waiting",
		"worktree_snapshot_waiting(",
		"worktree_snapshot_waiting()",
		"worktree_snapshot_waiting(local_directory=NuvioTV",
		"worktree_snapshot_waiting(holder=x)",
		// A newer format field: refuse rather than return a partial parse.
		"worktree_snapshot_waiting(holder=x, local_directory=y, since=5s)",
		"worktree_snapshot_waiting(holder x, local_directory=y)",
	} {
		if wait, ok := ParseWorktreeSnapshot(reason); ok {
			t.Errorf("ParseWorktreeSnapshot(%q) = %+v, want rejected", reason, wait)
		}
		if IsWorktreeSnapshot(reason) {
			t.Errorf("IsWorktreeSnapshot(%q) = true, want false", reason)
		}
	}
}

// TestParseWorktreeSnapshotHolderUnknown keeps the absent-holder case inside
// the format instead of turning it into a different shape.
func TestParseWorktreeSnapshotHolderUnknown(t *testing.T) {
	t.Parallel()

	wait, ok := ParseWorktreeSnapshot("worktree_snapshot_waiting(holder=-, local_directory=NuvioTV)")
	if !ok {
		t.Fatal("absent holder was rejected")
	}
	if wait.LocalDirectory != "NuvioTV" {
		t.Fatalf("local_directory = %q, want NuvioTV", wait.LocalDirectory)
	}
}
