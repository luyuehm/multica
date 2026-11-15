// Package waitreason defines the stable wire formats written into
// agent_task_queue.wait_reason.
//
// The column has several readers with different needs: the status pill renders
// it to a person, while board, failover and lock-heal code compare holds by
// kind. Two holds now share it — an in-place task queued behind the per-path
// mutex, and a worktree task waiting for a consistent snapshot of the same
// directory — so the worktree one carries a fixed, parseable shape instead of
// prose. Writer (daemon) and reader (server, clients) use these definitions
// rather than repeating the literal, which is what keeps the two ends from
// drifting apart.
package waitreason

import (
	"fmt"
	"strings"
)

// WorktreeSnapshotPrefix opens the worktree snapshot hold.
const WorktreeSnapshotPrefix = "worktree_snapshot_waiting("

// MaxValueLen bounds each embedded value so a pathological directory name
// cannot dominate the column or the status pill.
const MaxValueLen = 120

// WorktreeSnapshot is the structured form of a worktree snapshot hold:
//
//	worktree_snapshot_waiting(holder=<task-id>, local_directory=<name>)
//
// LocalDirectory is the directory's DISPLAY name, never its absolute path. This
// column is stored server-side and pushed to every client on the session, and
// an absolute path carries the account name into screenshots and screen shares.
type WorktreeSnapshot struct {
	// Holder is the task id holding the per-path mutex; "-" when unknown.
	Holder string
	// LocalDirectory is the display name of the directory being snapshotted.
	LocalDirectory string
}

// String renders the wire format. Both values pass through Value, so the
// result is always single-line and never contains the format's own
// delimiters.
func (w WorktreeSnapshot) String() string {
	return fmt.Sprintf("%sholder=%s, local_directory=%s)",
		WorktreeSnapshotPrefix, Value(w.Holder), Value(w.LocalDirectory))
}

// ParseWorktreeSnapshot reads back a reason written by String. ok is false for
// anything that is not exactly the documented shape, including the in-place
// format: callers use this to decide whether a hold IS a snapshot wait, so a
// lenient parser would mislabel the thing the format exists to distinguish.
func ParseWorktreeSnapshot(reason string) (WorktreeSnapshot, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(reason), WorktreeSnapshotPrefix)
	if !ok || !strings.HasSuffix(rest, ")") {
		return WorktreeSnapshot{}, false
	}
	var wait WorktreeSnapshot
	for _, part := range strings.Split(strings.TrimSuffix(rest, ")"), ", ") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return WorktreeSnapshot{}, false
		}
		switch key {
		case "holder":
			wait.Holder = value
		case "local_directory":
			wait.LocalDirectory = value
		default:
			// An unknown field means a newer format than this reader knows.
			// Refusing beats returning a partial answer that reads as complete.
			return WorktreeSnapshot{}, false
		}
	}
	if wait.LocalDirectory == "" {
		return WorktreeSnapshot{}, false
	}
	return wait, true
}

// Value makes s safe to embed in the parenthesised format: it replaces the
// three structural characters with '-' (keeping the value readable and the
// parser total instead of introducing escapes), collapses line breaks so the
// text stays one line wherever it is rendered, trims, and caps the length.
// An empty or all-structural value becomes "-" so the key is always present.
func Value(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		case ',', '(', ')', '=':
			return '-'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return "-"
	}
	if len(s) > MaxValueLen {
		// Byte-wise truncation could split a multi-byte rune; back off to the
		// last valid boundary so the result stays printable UTF-8.
		s = strings.ToValidUTF8(s[:MaxValueLen], "")
		s = strings.TrimSpace(s)
	}
	return s
}

// IsWorktreeSnapshot reports whether reason describes a snapshot hold. Cheap
// test for callers that only need the classification.
func IsWorktreeSnapshot(reason string) bool {
	_, ok := ParseWorktreeSnapshot(reason)
	return ok
}
