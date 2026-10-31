import { describe, expect, it } from "vitest";
import {
  parseWorktreeSnapshotWaitReason,
  WORKTREE_SNAPSHOT_PREFIX,
} from "./wait-reason";

// The parser mirrors server/pkg/waitreason. Both ends have to agree on the
// shape, or the pill shows `holder=` / `local_directory=` to the user.
describe("parseWorktreeSnapshotWaitReason", () => {
  it("parses the format the daemon writes", () => {
    expect(
      parseWorktreeSnapshotWaitReason(
        "worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=NuvioTV)",
      ),
    ).toEqual({ holder: "a1b2c3d4", localDirectory: "NuvioTV" });
  });

  it("surfaces an unknown holder instead of dropping the hold", () => {
    expect(
      parseWorktreeSnapshotWaitReason(
        "worktree_snapshot_waiting(holder=-, local_directory=NuvioTV)",
      ),
    ).toEqual({ holder: "-", localDirectory: "NuvioTV" });
  });

  it("tolerates surrounding whitespace", () => {
    expect(
      parseWorktreeSnapshotWaitReason(
        "  worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=my repo)  ",
      ),
    ).toEqual({ holder: "a1b2c3d4", localDirectory: "my repo" });
  });

  it("rejects the in-place prose reason", () => {
    // The in-place format shares the column. Returning a snapshot here would
    // relabel every in-place hold as a worktree wait.
    for (const reason of [
      undefined,
      "",
      "NuvioTV",
      "NuvioTV (held by task a1b2c3d4)",
      "local_directory /Users/me/repo (held by task a1b2c3d4)",
    ]) {
      expect(parseWorktreeSnapshotWaitReason(reason)).toBeNull();
    }
  });

  it("rejects malformed or newer shapes rather than half-parsing them", () => {
    // A partial parse reads as complete and would silently drop fields a newer
    // daemon added.
    for (const reason of [
      WORKTREE_SNAPSHOT_PREFIX,
      "worktree_snapshot_waiting(",
      "worktree_snapshot_waiting()",
      "worktree_snapshot_waiting(local_directory=NuvioTV",
      "worktree_snapshot_waiting(holder=x)",
      "worktree_snapshot_waiting(holder=x, local_directory=y, since=5s)",
      "worktree_snapshot_waiting(holder x, local_directory=y)",
    ]) {
      expect(parseWorktreeSnapshotWaitReason(reason)).toBeNull();
    }
  });
});
