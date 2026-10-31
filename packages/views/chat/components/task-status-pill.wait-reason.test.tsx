import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { describe, expect, it } from "vitest";
import enChat from "../../locales/en/chat.json";
import { TaskStatusPill } from "./task-status-pill";

const TEST_RESOURCES = { en: { chat: enChat } };

function renderPill(waitReason: string) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <TaskStatusPill
        pendingTask={{
          task_id: "task-1",
          status: "waiting_local_directory",
          created_at: "2026-10-31T00:00:00Z",
          wait_reason: waitReason,
          supports_queue: false,
          queued_tasks: [],
        }}
        taskMessages={[]}
        availability="online"
      />
    </I18nProvider>,
  );
}

// The worktree snapshot hold shares wait_reason with the in-place format. It is
// written as a machine-readable envelope so board/failover code can classify
// the hold; the pill is the one place a person reads it, so unwrapping belongs
// here. Rendering the envelope verbatim would show `holder=` and
// `local_directory=` instead of a sentence.
describe("TaskStatusPill wait reason rendering", () => {
  it("unwraps a worktree snapshot hold into a readable label", () => {
    renderPill(
      "worktree_snapshot_waiting(holder=a1b2c3d4, local_directory=NuvioTV)",
    );
    expect(
      screen.getByText("Waiting for a consistent snapshot of NuvioTV"),
    ).toBeTruthy();
    expect(screen.queryByText(/local_directory=/)).toBeNull();
    expect(screen.queryByText(/holder=/)).toBeNull();
  });

  it("still renders the in-place reason verbatim", () => {
    renderPill("NuvioTV (held by task a1b2c3d4)");
    expect(
      screen.getByText("Waiting for NuvioTV (held by task a1b2c3d4)"),
    ).toBeTruthy();
  });

  it("falls back to the bare label when no reason is stored", () => {
    renderPill("");
    expect(screen.getByText("Waiting for local directory")).toBeTruthy();
  });
});
