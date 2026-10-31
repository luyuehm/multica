// wait-reason.ts turns a `wait_reason` string into what the status pill shows.
//
// The column carries two hold formats. The in-place one is prose the daemon
// already localizes by hand (`NuvioTV (held by task a1b2c3d4)`) and is rendered
// as-is. The worktree snapshot one is a machine-readable envelope
// (`worktree_snapshot_waiting(holder=…, local_directory=…)`) written so board,
// failover and lock-heal code can classify the hold — but it is also stored in
// the same column and pushed to the same clients, so rendering it verbatim
// would put `holder=` and `local_directory=` in front of the user.
//
// The parser mirrors server/pkg/waitreason: same prefix, same `key=value`
// fields, same values-normalization rules. It is deliberately strict — an
// unrecognized shape returns null and the caller falls back to the raw text,
// which is never wrong for a format this build does not know.

export const WORKTREE_SNAPSHOT_PREFIX = "worktree_snapshot_waiting(";

export interface WorktreeSnapshotWait {
  /**
   * Full id of the task holding the directory — the daemon stamps the whole
   * task UUID here, unlike the in-place reason's hand-shortened `a1b2c3d4`, so
   * an operator can look the holder up without matching a prefix. "-" when the
   * holder had already released and its id is unknown.
   */
  holder: string;
  /** Display name of the directory being snapshotted. */
  localDirectory: string;
}

export function parseWorktreeSnapshotWaitReason(
  reason: string | undefined,
): WorktreeSnapshotWait | null {
  const trimmed = reason?.trim();
  if (!trimmed || !trimmed.startsWith(WORKTREE_SNAPSHOT_PREFIX)) return null;
  if (!trimmed.endsWith(")")) return null;

  const body = trimmed.slice(
    WORKTREE_SNAPSHOT_PREFIX.length,
    trimmed.length - 1,
  );
  let holder: string | undefined;
  let localDirectory: string | undefined;
  for (const part of body.split(", ")) {
    const eq = part.indexOf("=");
    if (eq < 0) return null;
    const key = part.slice(0, eq);
    const value = part.slice(eq + 1);
    switch (key) {
      case "holder":
        holder = value;
        break;
      case "local_directory":
        localDirectory = value;
        break;
      // A field this build does not know means a newer format. Refuse rather
      // than return a partial answer that reads as complete.
      default:
        return null;
    }
  }
  if (!localDirectory) return null;
  return { holder: holder ?? "-", localDirectory };
}
