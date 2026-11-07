import { expect, it } from "vitest";
import {
  actorOf,
  isActive,
  isFinished,
  isNeedsYou,
  isRunning,
  orderStatus,
  shipState,
  stageIndex,
  stageStatuses,
  stateInfo,
} from "../../src/composables/presentation";
import type { WorkOrder, WorkOrderState } from "../../src/types/api";
import { orders } from "../fixtures/workspace";

const wo = (state: WorkOrderState, extra: Partial<WorkOrder> = {}) =>
  ({ ...orders[0], state, clarif_question: "", ...extra }) as WorkOrder;
const PR = "https://example.test/pr/1",
  AT = "2026-10-25T00:00:00Z";

it("maps every backend state to one of the five stages", () => {
  const expected: Partial<Record<WorkOrderState, number>> = {
    CREATED: 0,
    REQUIREMENT_ANALYSIS: 0,
    TASK_DECOMPOSITION: 1,
    SPEC_AND_PLAN_GENERATION: 1,
    PLANNING: 1,
    ARCHITECT_REVIEW: 1,
    BLOCKED: 3,
    QUEUED: 2,
    IMPLEMENTING: 2,
    VERIFYING: 2,
    SPECIALIST_REVIEW: 3,
    PR_PREPARATION: 3,
    MERGE_READINESS: 3,
    NEEDS_HUMAN_REVIEW: 4,
    READY: 4,
  };
  for (const [state, stage] of Object.entries(expected))
    expect(stageIndex(wo(state as WorkOrderState)), state).toBe(stage);
});

it("places a cancelled order at its furthest run, a declined PR at Ship, and nowhere without runs", () => {
  expect(stageIndex(wo("CANCELLED"), [{ kind: "builder" }])).toBe(2);
  expect(
    stageIndex(
      wo("CANCELLED"),
      [{ kind: "builder" }],
      [{ kind: "pr_declined" }],
    ),
  ).toBe(4);
  expect(stageStatuses(wo("CANCELLED"), [], [{ kind: "pr_declined" }])[4]).toBe(
    "stop",
  );
  expect(stageStatuses(wo("BLOCKED"))[3]).toBe("stop");
  expect(
    stageIndex(wo("CANCELLED"), [{ kind: "architect" }, { kind: "inspector" }]),
  ).toBe(3);
  expect(stageStatuses(wo("CANCELLED"))).toEqual(Array(5).fill("todo"));
  expect(stageStatuses(wo("CANCELLED"), [{ kind: "builder" }])).toEqual([
    "done",
    "done",
    "stop",
    "todo",
    "todo",
  ]);
});

it("marks the stage that waits on a human and who holds the order", () => {
  expect(stageStatuses(wo("IMPLEMENTING"))).toEqual([
    "done",
    "done",
    "cur",
    "todo",
    "todo",
  ]);
  const plan = (plan_review: "pending" | "approved") =>
    stageStatuses(wo("ARCHITECT_REVIEW", { plan_review }))[1];
  expect(plan("pending")).toBe("cur");
  expect(plan("approved")).toBe("wait");
  const asked = wo("WAITING_FOR_HUMAN", { clarif_question: "How long?" });
  expect(stageStatuses(asked)[1]).toBe("wait");
  expect(stageIndex({ ...asked, clarif_answer: "a day" })).toBe(3);
  expect(stageStatuses(wo("WAITING_FOR_HUMAN"))[3]).toBe("wait");
  expect(stageStatuses(wo("NEEDS_HUMAN_REVIEW"))[4]).toBe("wait");
  expect(actorOf(wo("NEEDS_HUMAN_REVIEW"))).toBe("you");
  expect(actorOf(wo("IMPLEMENTING"))).toBe("builder");
  expect(actorOf(wo("VERIFYING"))).toBe("factory");
});

it("reports every Ship sub-state and finishes Ship when the PR is open", () => {
  const ready = (extra: Partial<WorkOrder>) => wo("READY", extra);
  const cases: [WorkOrder, string, string][] = [
    [wo("NEEDS_HUMAN_REVIEW"), "approval", "Waiting for your approval"],
    [ready({ pr_url: PR }), "open", "PR open · in review"],
    [
      ready({ pr_url: PR, pr_closed_at: AT }),
      "closed",
      "PR closed without merge",
    ],
    [
      ready({ pr_url: PR, pr_track_failed_at: AT }),
      "tracking_stopped",
      "PR tracking stopped",
    ],
    [ready({ pr_url: PR, pr_merged_at: AT }), "merged", "Merged"],
    [ready({}), "local", "Approved · no pull request"],
  ];
  for (const [order, key, label] of cases) {
    expect(shipState(order)?.key).toBe(key);
    expect(shipState(order)?.label).toBe(label);
    expect(orderStatus(order).label).toBe(label);
  }
  expect(shipState(wo("IMPLEMENTING"))).toBeNull();
  const open = ready({ pr_url: PR });
  expect(stageStatuses(open)).toEqual(Array(5).fill("done"));
  expect(isFinished(open)).toBe(true);
  expect(isActive(open)).toBe(false);
  expect(stageStatuses(ready({}))).toEqual(Array(5).fill("done"));
  const merged = ready({ pr_url: PR, pr_merged_at: AT });
  expect(stageStatuses(merged)).toEqual(Array(5).fill("done"));
  expect(isFinished(merged)).toBe(true);
});

it("treats merged and cancelled as finished, blocked as needing you; READY is no longer assembled", () => {
  expect(isFinished(wo("CANCELLED"))).toBe(true);
  expect(isFinished(wo("BLOCKED"))).toBe(false);
  expect(isFinished(wo("READY", { pr_url: PR }))).toBe(true);
  expect(isFinished(wo("READY"))).toBe(true);
  const stalled = [{ pr_closed_at: AT }, { pr_track_failed_at: AT }];
  for (const extra of stalled) {
    const w = wo("READY", { pr_url: PR, ...extra });
    expect(isNeedsYou(w)).toBe(true);
    expect(isRunning(w)).toBe(false);
    expect(isFinished(w)).toBe(false);
    expect(stageStatuses(w)[4]).toBe("wait");
  }
  expect(isRunning(wo("IMPLEMENTING"))).toBe(true);
  expect(isRunning(wo("READY", { pr_url: PR }))).toBe(false);
  expect(isNeedsYou(wo("BLOCKED"))).toBe(true);
  expect(isFinished(wo("NEEDS_HUMAN_REVIEW"))).toBe(false);
  expect(stateInfo("READY").label).not.toBe("Assembled");
});

it("keeps a PR that failed to open out of Ship", () => {
  const failed = [{ kind: "pr_open_failed" }];
  const w = wo("READY");
  expect(shipState(w, failed)).toMatchObject({
    key: "open_failed",
    tone: "red",
  });
  expect(stageStatuses(w, [], failed)[4]).toBe("cur");
  expect(shipState(wo("READY", { pr_url: PR }), failed)?.key).toBe("open");
});
