import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import ActivityPanel from "../../src/features/factory/runs/ActivityPanel.vue";
import {
  duration,
  groupActivity,
  liveRun,
  liveVerb,
  toolLine,
} from "../../src/features/factory/runs/agentActivity";
import type { WOEvent } from "../../src/types/api";

let id = 0;
const ev = (
  kind: string,
  payload?: Record<string, unknown>,
  actor = "architect",
  message = kind,
): WOEvent => ({
  id: ++id,
  work_order_id: "wo1",
  actor,
  kind,
  message,
  payload,
  created_at: new Date(Date.UTC(2026, 9, 24, 17, 0, id)).toISOString(),
});

describe("groupActivity", () => {
  it("folds a run's steps into one group and keeps other events apart", () => {
    const items = groupActivity([
      ev("state_changed", undefined, "foreman"),
      ev("agent_started", { run_id: "r1" }),
      ev("agent_step", {
        run_id: "r1",
        step: 1,
        tools: [{ name: "read", target: "D06.md" }],
        tokens_out: 40,
      }),
      ev("plan_revision_requested", undefined, "foreman"),
      ev("agent_step", {
        run_id: "r1",
        step: 2,
        tools: [{ name: "grep", target: "tenant", failed: true }],
        tokens_out: 60,
      }),
      ev("agent_completed", { run_id: "r1" }),
    ]);
    expect(items.map((i) => i.type)).toEqual(["event", "run", "event"]);
    const run = items[1];
    if (run.type !== "run") throw new Error("expected a run group");
    expect(run.status).toBe("finished");
    expect(run.steps.map((s) => s.n)).toEqual([1, 2]);
    expect(run.tokensOut).toBe(100);
    expect(liveRun(items)).toBeUndefined();
  });

  it("tracks stall, recovery and failure", () => {
    const stalled = groupActivity([
      ev("agent_started", { run_id: "r2" }),
      ev("agent_stalled", { run_id: "r2" }),
    ]);
    expect(liveRun(stalled)?.status).toBe("stalled");

    const recovered = groupActivity([
      ev("agent_started", { run_id: "r2" }),
      ev("agent_stalled", { run_id: "r2" }),
      ev("agent_step", { run_id: "r2", step: 1, tools: [] }),
    ]);
    expect(liveRun(recovered)?.status).toBe("running");

    const failed = groupActivity([
      ev("agent_started", { run_id: "r3" }),
      ev(
        "agent_failed",
        { run_id: "r3" },
        "foreman",
        "foreman agent failed: timeout",
      ),
    ]);
    const run = failed[0];
    expect(run.type === "run" && run.outcome).toBe(
      "foreman agent failed: timeout",
    );
    expect(liveRun(failed)).toBeUndefined();
  });

  it("leaves agent events without a run_id as plain events", () => {
    const items = groupActivity([ev("agent_started"), ev("agent_completed")]);
    expect(items.every((i) => i.type === "event")).toBe(true);
  });
});

describe("presentation", () => {
  it("phrases tools, verbs and durations", () => {
    expect(toolLine({ name: "grep", target: "tenant" })).toBe(
      "Searched for tenant",
    );
    expect(toolLine({ name: "todowrite" })).toBe("todowrite");
    const [run] = groupActivity([
      ev("agent_started", { run_id: "r4" }, "foreman"),
    ]);
    if (run.type !== "run") throw new Error("expected a run group");
    expect(liveVerb(run)).toBe("Foreman is reviewing…");
    run.status = "stalled";
    expect(liveVerb(run)).toBe("Foreman is waiting on the model…");
    const start = "2026-10-24T17:00:00.000Z";
    expect(duration(start, Date.parse(start) + 42_000)).toBe("42s");
    expect(duration(start, Date.parse(start) + 192_000)).toBe("3m 12s");
  });
});

describe("ActivityPanel agent runs", () => {
  const finished = [
    ev("agent_started", { run_id: "f1" }, "foreman"),
    ev(
      "agent_step",
      { run_id: "f1", step: 1, tools: [{ name: "read", target: "D06.md" }] },
      "foreman",
    ),
    ev("agent_completed", { run_id: "f1" }, "foreman"),
  ];

  it("shows the running agent live with its latest steps", () => {
    const w = mount(ActivityPanel, {
      props: {
        runs: [],
        events: [
          ...finished,
          ev("agent_started", { run_id: "a1" }),
          ev("agent_step", {
            run_id: "a1",
            step: 1,
            tools: [{ name: "glob", target: "**/*D06*" }],
          }),
        ],
      },
    });
    const live = w.find('[data-test="agent-live"]');
    expect(live.text()).toContain("Architect is drafting the plan…");
    expect(live.text()).toContain("Listed files matching **/*D06*");
    expect(w.find('[data-test="agent-stalled"]').exists()).toBe(false);
    const done = w.findAll('[data-test="agent-run"]');
    expect(done).toHaveLength(1);
    expect(done[0].text()).toContain("Read D06.md");
    expect(done[0].attributes("open")).toBeUndefined();
    w.unmount();
  });

  it("warns when the running agent stalls and drops the card when it ends", async () => {
    const running = [
      ev("agent_started", { run_id: "a2" }),
      ev("agent_stalled", { run_id: "a2" }),
    ];
    const w = mount(ActivityPanel, { props: { runs: [], events: running } });
    expect(w.find('[data-test="agent-stalled"]').exists()).toBe(true);
    expect(w.text()).toContain("Architect is waiting on the model…");
    await w.setProps({
      events: [
        ...running,
        ev(
          "agent_failed",
          { run_id: "a2" },
          "architect",
          "architect agent failed: timeout",
        ),
      ],
    });
    expect(w.find('[data-test="agent-live"]').exists()).toBe(false);
    expect(w.find('[data-test="agent-run"]').text()).toContain(
      "architect agent failed: timeout",
    );
    w.unmount();
  });
});
