"use client";

import { cn } from "@multica/ui/lib/utils";
import { ShimmerText } from "@multica/ui/components/common/shimmer-text";
import { UnicodeSpinner } from "@multica/ui/components/common/unicode-spinner";
import type { AgentAvailability } from "@multica/core/agents";
import { parseWorktreeSnapshotWaitReason } from "@multica/core/chat/wait-reason";
import type { TaskMessagePayload } from "@multica/core/types";
import { useT } from "../../i18n";
import { pickStageKeys } from "./pick-stage";

interface Props {
  /** Task lifecycle status (`running`, `dispatched`, `queued`, …). */
  status: string | undefined;
  /** Live task-message stream — the latest non-error entry picks the label. */
  taskMessages: readonly TaskMessagePayload[];
  /** Resolved agent presence; pass `undefined` to skip availability hints. */
  availability?: AgentAvailability | undefined;
  /** Transient hint from a `task:activity` event (e.g. "reconnecting"). */
  activity?: string;
  /** What a parked task is waiting on, when the server named it. */
  waitReason?: string;
  /** Suppress the built-in spinner when the caller renders its own. */
  hideSpinner?: boolean;
  className?: string;
}

// AgentActivityLabel renders the live "what is the agent doing" label —
// "Reading files", "Thinking", "Typing", etc. — derived from the streamed
// task messages. Shared by the chat status pill and the issue task panel so
// both surface the same fine-grained activity instead of a generic
// "working" banner that looks frozen between tool calls.
export function AgentActivityLabel({
  status,
  taskMessages,
  availability,
  activity,
  waitReason,
  hideSpinner,
  className,
}: Props) {
  const { t } = useT("common");
  const decision = pickStageKeys(status, taskMessages, availability, activity);
  // A parked task that names what it is parked on turns an unexplained wait
  // into an actionable one: the user can see it is a sibling task, not a hung
  // agent, and decide whether cancelling is worth it. Older servers send no
  // reason, so the bare label has to stay reachable.
  const reason = waitReason?.trim();
  // The worktree snapshot hold arrives as a machine-readable envelope
  // (see core/chat/wait-reason). It is written that way so board/failover
  // code can classify the hold, which means the label has to unwrap it —
  // shown verbatim, the user would read `holder=` and
  // `local_directory=` instead of "a snapshot of NuvioTV".
  const snapshot =
    decision.needsWaitReason && reason
      ? parseWorktreeSnapshotWaitReason(reason)
      : null;
  const label = decision.toolKey
    ? t(($) => $.status_pill.tools[decision.toolKey!])
    : snapshot
      ? t(($) => $.status_pill.stages.worktree_snapshot_waiting_reason, {
          reason: snapshot.localDirectory,
        })
      : decision.needsWaitReason && reason
        ? t(($) => $.status_pill.stages.waiting_local_directory_reason, {
            reason,
          })
        : t(($) => $.status_pill.stages[decision.stageKey]);
  // toolKey labels are always "actively working"; only explicit stage
  // decisions carry the static flag (queued / waiting / offline).
  const isStatic = decision.toolKey ? false : decision.static === true;

  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)}>
      {!hideSpinner && !isStatic && (
        <UnicodeSpinner name="breathe" className="opacity-70" />
      )}
      <span className="truncate">
        <ShimmerText active={!isStatic}>{label}</ShimmerText>
      </span>
    </span>
  );
}
