package service

import (
	"strings"
	"testing"
)

// TestSanitizeWaitReason covers the version-skew guard on the hold hint.
//
// The leak this prevents is one that surfacing the field CREATED: before the
// StatusPill read wait_reason, an old daemon's absolute path sat unread in a
// column. A new server paired with an old daemon would now render it, so the
// legacy format has to die at the write boundary — the one place both the REST
// payload and the websocket event read through.
func TestSanitizeWaitReason(t *testing.T) {
	t.Parallel()

	t.Run("drops the legacy absolute-path format", func(t *testing.T) {
		cases := []string{
			"local_directory /Users/foxace/repos/NuvioTV",
			"local_directory /Users/foxace/repos/NuvioTV (held by task a1b2c3d4)",
			"local_directory /home/foxace/src/app (held by task a1b2c3d4)",
			`local_directory C:\Users\foxace\repos\NuvioTV`,
			`local_directory c:/Users/foxace/repos/NuvioTV (held by task a1b2c3d4)`,
			`local_directory \\fileserver\share\repo`,
			"  local_directory /Users/foxace/repos/NuvioTV  ",
		}
		for _, reason := range cases {
			if got := sanitizeWaitReason(reason); got != "" {
				t.Errorf("sanitizeWaitReason(%q) = %q, want empty — a path reached the client", reason, got)
			}
		}
	})

	t.Run("keeps the display-name format current daemons send", func(t *testing.T) {
		cases := map[string]string{
			"NuvioTV":                             "NuvioTV",
			"NuvioTV (held by task a1b2c3d4)":     "NuvioTV (held by task a1b2c3d4)",
			"  NuvioTV (held by task a1b2c3d4)  ": "NuvioTV (held by task a1b2c3d4)",
			"my repo with spaces":                 "my repo with spaces",
			"team/service-api":                    "team/service-api",
			"":                                    "",
		}
		for reason, want := range cases {
			if got := sanitizeWaitReason(reason); got != want {
				t.Errorf("sanitizeWaitReason(%q) = %q, want %q", reason, got, want)
			}
		}
	})

	t.Run("keeps a directory genuinely named local_directory", func(t *testing.T) {
		// Current daemons send label-or-basename, so this is what a directory
		// actually called "local_directory" produces. The guard must key on the
		// path that follows the legacy prefix, not on the word alone.
		for _, reason := range []string{
			"local_directory",
			"local_directory (held by task a1b2c3d4)",
		} {
			if got := sanitizeWaitReason(reason); got != reason {
				t.Errorf("sanitizeWaitReason(%q) = %q, want it kept", reason, got)
			}
		}
	})

	// The worktree snapshot format carries the same "no absolute paths" rule as
	// the in-place one, but its value sits after `local_directory=`, where the
	// legacy prefix check cannot see it. A daemon that regressed to a path there
	// would ship it inside a perfectly well-formed envelope.
	t.Run("keeps the worktree snapshot format current daemons send", func(t *testing.T) {
		for _, reason := range []string{
			"worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=NuvioTV)",
			"worktree_snapshot_waiting(holder=-, local_directory=NuvioTV)",
			"  worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=my repo)  ",
			"worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=local_directory)",
		} {
			if got := sanitizeWaitReason(reason); got != strings.TrimSpace(reason) {
				t.Errorf("sanitizeWaitReason(%q) = %q, want it kept", reason, got)
			}
		}
	})

	t.Run("drops a worktree snapshot format that carries a path", func(t *testing.T) {
		for _, reason := range []string{
			"worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=/Users/foxace/repos/NuvioTV)",
			`worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=C:\Users\foxace\repos\NuvioTV)`,
			`worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=\\fileserver\share\repo)`,
		} {
			if got := sanitizeWaitReason(reason); got != "" {
				t.Errorf("sanitizeWaitReason(%q) = %q, want empty — a path reached the client", reason, got)
			}
		}
	})
}
