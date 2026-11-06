import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import WorkOrderView from "../../src/features/factory/work-orders/WorkOrderView.vue";
import ReviewPanel from "../../src/features/factory/reviews/ReviewPanel.vue";
import ChangesPanel from "../../src/features/factory/reviews/ChangesPanel.vue";
import { parseUnifiedDiff } from "../../src/features/factory/reviews/diff";
import {
  architectReport,
  changeRequestDraft,
  escalationReason,
} from "../../src/features/factory/runs/agentActivity";
import { subscribe } from "../../src/services/events/sse";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import type { Run, WOEvent, WorkOrder } from "../../src/types/api";
import { orders, projects, runs, tasks, user } from "../fixtures/workspace";

const PR = "https://example.test/acme/repo/pull/7",
  AT = "2026-10-25T00:00:00Z";
const event = (
  kind: string,
  message: string,
  payload?: Record<string, unknown>,
  actor = "factory",
): WOEvent => ({
  id: Math.random(),
  work_order_id: "LF-121",
  actor,
  kind,
  message,
  payload,
  created_at: AT,
});

describe("live PR events", () => {
  it("subscribes to pr_merged, pr_closed and pr_track_failed", () => {
    const kinds: string[] = [];
    class Fake {
      addEventListener(k: string) {
        kinds.push(k);
      }
      close() {}
    }
    vi.stubGlobal("EventSource", Fake);
    subscribe("/x", () => {})();
    expect(kinds).toEqual(
      expect.arrayContaining(["pr_merged", "pr_closed", "pr_track_failed"]),
    );
  });
});

describe("work order page ship states", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.stubGlobal(
      "EventSource",
      class {
        addEventListener() {}
        close() {}
      },
    );
    useSession().user = user;
  });
  async function render(
    state: WorkOrder["state"],
    extra: Partial<WorkOrder>,
    evs: WOEvent[] = [],
    tab?: string,
  ) {
    const f = useFactory();
    f.projects = projects;
    vi.spyOn(f, "loadDetail").mockImplementation(async (id) => {
      f.selectedID = id;
      f.detail = {
        work_order: { ...orders[4], state, ...extra },
        plan: tasks,
        runs,
      };
      f.events = evs;
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/factory", component: { render: () => null } },
        { path: "/factory/:id", component: WorkOrderView },
      ],
    });
    await router.push(`/factory/LF-121${tab ? `?tab=${tab}` : ""}`);
    await router.isReady();
    const w = mount(WorkOrderView, { global: { plugins: [router] } });
    await flushPromises();
    return w;
  }

  it("lands on Changes & PR at Ship and shows the open PR with a link", async () => {
    const w = await render("READY", { pr_url: PR, pr_ref: "acme/repo#7" });
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe(
      "Changes & PR",
    );
    expect(w.find('[data-test="ship-state"]').text()).toContain(
      "PR open · in review",
    );
    expect(w.find('[data-test="pr-link"]').attributes("href")).toBe(PR);
    expect(w.find('[data-test="now-card"]').text()).toContain(
      "PR open · in review",
    );
    expect(
      w.find('[data-test="stage-track"] [data-stage="Ship"]').classes(),
    ).toContain("cur");
    w.unmount();
  });

  it.each([
    [{ pr_url: PR, pr_closed_at: AT }, "PR closed without merge", "wait"],
    [{ pr_url: PR, pr_track_failed_at: AT }, "PR tracking stopped", "wait"],
    [{ pr_url: PR, pr_merged_at: AT }, "Merged", "done"],
  ])("shows %j as %s", async (extra, label, stage) => {
    const w = await render("READY", extra);
    expect(w.find('[data-test="ship-state"]').text()).toContain(label);
    expect(
      w.find('[data-test="stage-track"] [data-stage="Ship"]').classes(),
    ).toContain(stage);
    w.unmount();
  });

  it("explains why the converge gate escalated and offers the decisions on Changes & PR", async () => {
    const gate = event(
      "converge_gate",
      "Converge gate escalated",
      { outcome: "escalate", row: 2, note: "No acceptance criteria recorded" },
      "jev",
    );
    const w = await render("NEEDS_HUMAN_REVIEW", {}, [gate], "changes");
    expect(w.find('[data-test="ship-state"]').text()).toContain(
      "Waiting for your approval",
    );
    expect(w.find('[data-test="ship-state"]').text()).toContain(
      "No acceptance criteria recorded",
    );
    expect(
      w.find('[data-test="pr-approve"]').attributes("disabled"),
    ).toBeUndefined();
    w.unmount();
  });

  it("keeps the old tab names working and redirects nothing visible", async () => {
    const w = await render("IMPLEMENTING", {}, [], "graph");
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe(
      "Progress",
    );
    expect(w.find(".tg").exists()).toBe(true);
    w.unmount();
  });
});

describe("escalation and Architect helpers", () => {
  it("returns the latest escalating gate or plan escalation, ignoring approvals", () => {
    expect(
      escalationReason([
        event("plan_escalated", "The plan touches a database schema"),
        event("converge_gate", "ok", { outcome: "converged", row: 5 }, "jev"),
      ]),
    ).toBe("The plan touches a database schema");
    expect(
      escalationReason([
        event("converge_gate", "x", { outcome: "escalate", note: "n1" }, "jev"),
        event("converge_gate", "x", { outcome: "escalate", note: "n2" }, "jev"),
      ]),
    ).toBe("n2");
    expect(escalationReason([])).toBe("");
  });
  it("reads the latest converge report", () => {
    const report = architectReport([
      event("converge_report", "a", { revision: 1, round: 1, gaps: [] }),
      event("converge_report", "b", {
        revision: 2,
        round: 2,
        gaps: [
          {
            id: "C2-G1",
            type: "missing",
            layer: "code",
            cites: "c1",
            evidence: "e",
            fix: "f",
          },
        ],
        converged: false,
      }),
    ]);
    expect(report?.revision).toBe(2);
    expect(report?.gaps.map((g) => g.id)).toEqual(["C2-G1"]);
    expect(architectReport([])).toBeNull();
  });
  it("renders gaps in the Review tab and drafts the change request from their fixes", () => {
    const payload = {
      revision: 2,
      round: 1,
      gaps: [
        {
          id: "C1-G1",
          type: "missing",
          layer: "code",
          cites: "c2",
          evidence: "no retry test",
          fix: "give each section a budget",
        },
      ],
      converged: false,
    };
    const w = mount(ReviewPanel, {
      props: {
        checks: [],
        findings: [],
        revision: 2,
        events: [event("converge_report", "b", payload)],
      },
    });
    const text = w.find('[data-test="architect-verdicts"]').text();
    expect(text).toContain("C1-G1");
    expect(text).toContain("missing");
    expect(text).toContain("no retry test");
    expect(w.find('[data-test="verdict-fix"]').text()).toContain(
      "give each section a budget",
    );
    expect(changeRequestDraft(architectReport(w.props("events")!))).toBe(
      "c2: give each section a budget",
    );
    expect(changeRequestDraft(null)).toBe("");
  });
});

describe("diff", () => {
  const patch = [
    "diff --git a/a.go b/a.go",
    "index 1..2 100644",
    "--- a/a.go",
    "+++ b/a.go",
    "@@ -1,3 +1,4 @@",
    " package a",
    "-var x = 1",
    "-- not a header",
    "+var x = 2",
    "+var y = 3",
    " ",
    "diff --git a/new.txt b/new.txt",
    "new file mode 100644",
    "--- /dev/null",
    "+++ b/new.txt",
    "@@ -0,0 +1,2 @@",
    "+one",
    "+two",
  ].join("\n");
  it("parses files, counts and line numbers without mistaking a deleted '-- ' line for a header", () => {
    const files = parseUnifiedDiff(patch);
    expect(files.map((f) => [f.path, f.add, f.del])).toEqual([
      ["a.go", 2, 2],
      ["new.txt", 2, 0],
    ]);
    const lines = files[0].hunks[0].lines;
    expect(lines[1]).toMatchObject({ t: "del", o: 2, s: "var x = 1" });
    expect(lines[2]).toMatchObject({ t: "del", o: 3, s: "- not a header" });
    expect(lines[3]).toMatchObject({ t: "add", n: 2 });
  });
  const builder = (output: string): Run => ({
    ...runs[0],
    id: "r",
    output,
  });
  it("renders a file list, collapses files and switches between unified and split", async () => {
    const w = mount(ChangesPanel, {
      props: {
        runs: [builder(patch)],
        state: "NEEDS_HUMAN_REVIEW",
        canWrite: true,
        busy: false,
      },
    });
    expect(w.findAll('[data-test="diff-file"]')).toHaveLength(2);
    expect(w.find('[data-test="diff-total"]').text()).toContain(
      "2 files changed",
    );
    expect(w.find(".fx-dtable.unified").exists()).toBe(true);
    await w.findAll(".fx-seg button")[1].trigger("click");
    expect(w.find(".fx-dtable.split").exists()).toBe(true);
    expect(w.find(".fx-sl").exists()).toBe(true);
    await w.find('[aria-label="Collapse a.go"]').trigger("click");
    expect(w.findAll(".fx-dtable")).toHaveLength(1);
    expect(w.text()).toContain("Builder output");
    expect(w.find("details.fx-card").attributes("open")).toBeUndefined();
  });
  it("falls back to recorded builder output when no run printed a diff", () => {
    const w = mount(ChangesPanel, {
      props: { runs, state: "IMPLEMENTING", canWrite: true, busy: false },
    });
    expect(w.text()).toContain("Builder output");
    expect(w.find('[data-test="diff-file"]').exists()).toBe(false);
  });
});
