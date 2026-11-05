import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import WorkOrderView from "../../src/features/factory/work-orders/WorkOrderView.vue";
import ChangesPanel from "../../src/features/factory/reviews/ChangesPanel.vue";
import {
  diffFromRuns,
  parseUnifiedDiff,
} from "../../src/features/factory/reviews/diff";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import type { Run, WOEvent, WorkOrder } from "../../src/types/api";
import { orders, projects, runs, tasks, user } from "../fixtures/workspace";

const run = (output: string, extra: Partial<Run> = {}): Run => ({
  ...runs[0],
  id: Math.random().toString(),
  output,
  ...extra,
});

describe("diff parsing edge cases", () => {
  it("handles CRLF and strips the tab and timestamp from header paths", () => {
    const text = [
      "--- a/x.go\t2026-11-05 10:00:00.000000000 +0700",
      "+++ b/x.go\t2026-11-05 10:01:00.000000000 +0700",
      "@@ -1,1 +1,1 @@",
      "-old",
      "+new",
      "",
    ].join("\r\n");
    const [f] = parseUnifiedDiff(text);
    expect(f.path).toBe("x.go");
    expect([f.add, f.del]).toEqual([1, 1]);
    expect(f.hunks[0].lines[1].s).toBe("new");
  });
  it("starts a new file at a ---/+++/@@ triple even when hunk counts are overstated", () => {
    const text = [
      "--- a/a.go",
      "+++ b/a.go",
      "@@ -1,5 +1,5 @@",
      "-a",
      "+b",
      "--- a/b.go",
      "+++ b/b.go",
      "@@ -1,1 +1,1 @@",
      "-c",
      "+d",
    ].join("\n");
    expect(parseUnifiedDiff(text).map((f) => [f.path, f.add, f.del])).toEqual([
      ["a.go", 1, 1],
      ["b.go", 1, 1],
    ]);
  });
  it("keeps rename-only, binary and mode-only files in the list and totals", () => {
    const text = [
      "diff --git a/old.txt b/new.txt",
      "similarity index 100%",
      "rename from old.txt",
      "rename to new.txt",
      "diff --git a/logo.png b/logo.png",
      "Binary files a/logo.png and b/logo.png differ",
      "diff --git a/run.sh b/run.sh",
      "old mode 100644",
      "new mode 100755",
    ].join("\n");
    const files = parseUnifiedDiff(text);
    expect(files.map((f) => f.path)).toEqual(["new.txt", "logo.png", "run.sh"]);
    expect(files[0].note).toContain("renamed from old.txt");
    expect(files[1].note).toBe("binary file");
    expect(files[2].note).toContain("100755");
    expect(files.every((f) => f.hunks.length === 0)).toBe(true);
  });
  it("appends hunks per path across runs and skips failed runs", () => {
    const h = (n: number) =>
      `diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -${n},1 +${n},1 @@\n-x\n+y`;
    const files = diffFromRuns([
      run(h(1)),
      run(h(10), { state: "failed" }),
      run(h(20)),
    ]);
    expect(files).toHaveLength(1);
    expect(files[0].hunks.map((x) => x.header)).toEqual([
      "@@ -1,1 +1,1 @@",
      "@@ -20,1 +20,1 @@",
    ]);
    expect([files[0].add, files[0].del]).toEqual([2, 2]);
  });
  it("lists note-only files in the viewer and its totals", () => {
    const w = mount(ChangesPanel, {
      props: {
        runs: [
          run(
            "diff --git a/logo.png b/logo.png\nBinary files a/logo.png and b/logo.png differ",
          ),
        ],
        state: "IMPLEMENTING",
        canWrite: true,
        busy: false,
      },
    });
    expect(w.find('[data-test="diff-total"]').text()).toContain(
      "1 file changed",
    );
    expect(w.find('[data-test="diff-file"]').text()).toContain("binary file");
  });
});

describe("Changes & PR decisions", () => {
  it("offers decisions only at NEEDS_HUMAN_REVIEW and never on BLOCKED", () => {
    const at = (state: string) =>
      mount(ChangesPanel, {
        props: { runs, state, canWrite: true, busy: false },
      });
    expect(at("BLOCKED").find('[data-test="pr-decline"]').exists()).toBe(false);
    expect(
      at("NEEDS_HUMAN_REVIEW")
        .find('[data-test="pr-decline"]')
        .attributes("disabled"),
    ).toBeUndefined();
  });
});

describe("Now card and page", () => {
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
  ) {
    const f = useFactory();
    f.projects = projects;
    vi.spyOn(f, "loadDetail").mockImplementation(async (id) => {
      f.selectedID = id;
      f.detail = {
        work_order: { ...orders[0], state, clarif_question: "", ...extra },
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
    await router.push("/factory/LF-128");
    await router.isReady();
    const w = mount(WorkOrderView, { global: { plugins: [router] } });
    await flushPromises();
    return w;
  }
  const ev = (kind: string, message: string): WOEvent => ({
    id: Math.random(),
    work_order_id: "LF-128",
    actor: "factory",
    kind,
    message,
    created_at: "2026-10-25T00:00:00Z",
  });

  it("BLOCKED hides Pause and Cancel and shows the readiness reason", async () => {
    const w = await render("BLOCKED", {}, [
      ev("readiness_blocked", "2 blocker findings are open"),
    ]);
    expect(w.find('[data-test="blocked-reason"]').text()).toContain(
      "2 blocker findings are open",
    );
    expect(w.find('[data-test="execution-pause"]').exists()).toBe(false);
    expect(w.find('[data-test="cancel-order"]').exists()).toBe(false);
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe("Review");
    w.unmount();
  });
  it("hides Cancel at NEEDS_HUMAN_REVIEW", async () => {
    const w = await render("NEEDS_HUMAN_REVIEW", {});
    expect(w.find('[data-test="cancel-order"]').exists()).toBe(false);
    w.unmount();
  });
  it("sends an Inspector escalation (no open question) to Review", async () => {
    const w = await render("WAITING_FOR_HUMAN", {}, [
      ev("escalated", "Blocker findings remain after max revision cycles"),
    ]);
    expect(w.find('[data-test="now-card"]').text()).toContain(
      "The Inspector escalated",
    );
    expect(w.find('[data-test="now-card"]').text()).toContain(
      "Blocker findings remain",
    );
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe("Review");
    expect(w.find('[data-test="clarify"]').exists()).toBe(false);
    w.unmount();
  });
  it("shows the answered clarification on the Plan tab and the crew models", async () => {
    const w = await render("QUEUED", {
      clarif_question: "How long?",
      clarif_answer: "24 hours",
      builder_model: "mock/builder",
    });
    await w.findAll('[role="tab"]')[0].trigger("click");
    await flushPromises();
    expect(w.find('[data-test="decision"]').text()).toContain("24 hours");
    expect(w.find('[data-test="crew-models"]').text()).toContain(
      "Builder: mock/builder",
    );
    w.unmount();
  });
});
