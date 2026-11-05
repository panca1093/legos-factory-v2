import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ActivityPanel from "../../src/features/factory/runs/ActivityPanel.vue";
import {
  duration,
  groupActivity,
  liveRuns,
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
    expect(liveRuns(items)).toEqual([]);
  });

  it("tracks stall, recovery and failure", () => {
    const stalled = groupActivity([
      ev("agent_started", { run_id: "r2" }),
      ev("agent_stalled", { run_id: "r2" }),
    ]);
    expect(liveRuns(stalled)[0]?.status).toBe("stalled");

    const recovered = groupActivity([
      ev("agent_started", { run_id: "r2" }),
      ev("agent_stalled", { run_id: "r2" }),
      ev("agent_step", { run_id: "r2", step: 1, tools: [] }),
    ]);
    expect(liveRuns(recovered)[0]?.status).toBe("running");

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
    expect(liveRuns(failed)).toEqual([]);
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
    expect(toolLine({ name: "shell", target: "go test ./..." })).toBe(
      "Ran go test ./...",
    );
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

describe("steering and plan progress", () => {
  it("records a steered message in its run and shows every live run", async () => {
    const steer = vi.fn().mockResolvedValue(undefined);
    const w = mount(ActivityPanel, {
      props: {
        runs: [],
        tasks: [
          {
            id: "T1",
            work_order_id: "wo1",
            seq: 1,
            title: "Tax",
            objective: "",
            role: "builder",
            deps: [],
          },
          {
            id: "T2",
            work_order_id: "wo1",
            seq: 2,
            title: "Notes",
            objective: "",
            role: "builder",
            deps: [],
          },
        ],
        steer,
        events: [
          ev("agent_started", { run_id: "b1", task_id: "T1" }, "builder"),
          ev("agent_started", { run_id: "b2", task_id: "T2" }, "builder"),
          ev("agent_steered", { run_id: "b1" }, "human", "use util.ts"),
        ],
      },
    });
    const cards = w.findAll('[data-test="agent-live"]');
    expect(cards).toHaveLength(2);
    expect(cards[0].text()).toContain("Tax");
    expect(cards[0].text()).toContain("You: use util.ts");
    expect(cards[1].text()).toContain("Notes");
    await cards[1].find('[data-test="steer-input"]').setValue("keep it small");
    await cards[1].find("form").trigger("submit");
    await flushPromises();
    expect(steer).toHaveBeenCalledWith("b2", "keep it small");
    w.unmount();
  });

  it("offers a retry only on a failed task when retrying is allowed", async () => {
    const tasks = [
      {
        id: "T1",
        work_order_id: "wo1",
        seq: 1,
        title: "Tax",
        objective: "",
        role: "builder",
        deps: [],
      },
      {
        id: "T2",
        work_order_id: "wo1",
        seq: 2,
        title: "Notes",
        objective: "",
        role: "builder",
        deps: [],
      },
    ];
    const run = (task_id: string, state: string, error = "") => ({
      id: task_id,
      task_id,
      kind: "builder",
      state,
      harness: "claude-acp",
      error,
      tokens_in: 0,
      tokens_out: 0,
      cost_micros: 0,
      created_at: "",
    });
    const runs = [
      run("T1", "completed"),
      run("T2", "failed", "conflict markers left"),
    ];
    const w = mount(ActivityPanel, {
      props: { events: [], runs, tasks, canRetry: true },
    });
    expect(w.find('[data-test="plan-progress"]').text()).toContain(
      "1 / 2 merged",
    );
    expect(w.find('[data-task="T2"]').text()).toContain(
      "conflict markers left",
    );
    const retry = w.findAll('[data-test="retry-task"]');
    expect(retry).toHaveLength(1);
    await retry[0].trigger("click");
    expect(w.emitted("retry")).toEqual([["T2"]]);
    await w.setProps({ canRetry: false });
    expect(w.find('[data-test="retry-task"]').exists()).toBe(false);
    w.unmount();
  });
});
