import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, expect, it, vi } from "vitest";
import OverviewView from "../../src/features/overview/OverviewView.vue";
import { heatCells } from "../../src/features/overview/heatmap";
import {
  isRunning,
  isTodo,
  needsYou,
} from "../../src/composables/presentation";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { overview, orders, projects, user } from "../fixtures/workspace";
import type { UsageSummary } from "../../src/types/api";
const usage: UsageSummary = {
  days: 10,
  from: "2026-11-01",
  to: "2026-11-10",
  activity: Array.from({ length: 10 }, (_, i) => ({
    date: `2026-11-${String(i + 1).padStart(2, "0")}`,
    count: i === 2 ? 4 : i === 8 ? 2 : 0,
  })),
  roles: [
    {
      role: "builder",
      tokens_in: 120000,
      tokens_out: 8000,
      cost_micros: 450000,
      runs: 12,
    },
  ],
  month_to_date: {
    month: "2026-11",
    tokens_in: 1000,
    tokens_out: 500,
    cost_micros: 450000,
    runs: 12,
  },
};
beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.overview = structuredClone(overview);
  f.workOrders = structuredClone(orders);
  f.projects = projects;
  f.usage = structuredClone(usage);
  useSession().user = user;
  vi.spyOn(f, "refreshOverview").mockResolvedValue();
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
  vi.spyOn(f, "refreshUsage").mockResolvedValue();
});
const render = () =>
  mount(OverviewView, {
    global: { stubs: { RouterLink: { template: "<a><slot/></a>" } } },
  });
it("lists work orders waiting on a human and the running ones", async () => {
  const w = render();
  await flushPromises();
  expect(w.find("h1").text()).toContain("Panca");
  const needs = w.find('[aria-labelledby="needs-you"]');
  expect(needs.findAll(".lg-row")).toHaveLength(2);
  expect(needs.text()).toContain("Ship team invitation flow");
  expect(needs.text()).toContain("Answer clarification");
  expect(w.find('[aria-labelledby="running-now"]').text()).toContain(
    "Add accessible product search",
  );
  w.unmount();
});
it("renders usage by agent, the month-to-date card and heatmap modes", async () => {
  const w = render();
  await flushPromises();
  expect(w.find(".agent-row").text()).toContain("Builder");
  expect(w.find(".agent-row").text()).toContain("12 runs");
  expect(w.find(".agent-row").text()).toContain("$0.45");
  expect(w.find(".kpis").text()).toContain("1.5K tokens");
  expect(w.findAll(".heat i")).toHaveLength(10);
  await w.findAll(".mode-seg button")[2].trigger("click");
  expect(w.findAll(".heat i")[9].attributes("title")).toContain("6 total");
  w.unmount();
});
it("shows empty states when there is no usage", async () => {
  const f = useFactory();
  f.usage = { ...usage, activity: [], roles: [] };
  f.workOrders = [];
  const w = render();
  await flushPromises();
  expect(w.text()).toContain("No agent runs yet");
  expect(w.text()).toContain("No agent usage yet");
  expect(w.text()).toContain("Nothing waits on you");

  w.unmount();
});
it("does not raise a page alert when usage fails", async () => {
  const f = useFactory();
  f.usage = null;
  const w = render();
  await flushPromises();
  expect(w.find('[role="alert"]').exists()).toBe(false);
  expect(w.text()).toContain("Usage unavailable");
  w.unmount();
});
it("counts shipped orders by merged PR and does not refetch on first visit", async () => {
  const f = useFactory();
  f.usage = null;
  f.workOrders[0].pr_merged_at = "2026-10-24T00:00:00Z";
  const w = render();
  await flushPromises();
  expect(w.find(".kpis").text()).toContain("Shipped");
  expect(w.findAll(".lg-stat .value")[2].text()).toBe("1");
  expect(f.refreshUsage).not.toHaveBeenCalled();
  expect(f.refreshOverview).not.toHaveBeenCalled();
  w.unmount();
});
const withState = (state: string, plan_review?: string) =>
  ({ ...orders[0], state, plan_review }) as (typeof orders)[number];
it("needsYou keeps human gates and only escalated plan reviews", () => {
  const list = [
    withState("WAITING_FOR_HUMAN"),
    withState("ARCHITECT_REVIEW", "pending"),
    withState("ARCHITECT_REVIEW", "approved"),
    withState("NEEDS_HUMAN_REVIEW"),
    withState("BLOCKED"),
    withState("IMPLEMENTING"),
    withState("READY"),
  ];
  expect(needsYou(list).map((w) => [w.state, w.plan_review])).toEqual([
    ["WAITING_FOR_HUMAN", undefined],
    ["ARCHITECT_REVIEW", "approved"],
    ["NEEDS_HUMAN_REVIEW", undefined],
    ["BLOCKED", undefined],
  ]);
});
it("running includes planning and unescalated plan reviews but not orders waiting to be picked up", () => {
  expect(
    [
      withState("CREATED"),
      withState("QUEUED"),
      withState("PLANNING"),
      withState("ARCHITECT_REVIEW", "pending"),
      withState("ARCHITECT_REVIEW", "approved"),
      withState("BLOCKED"),
      withState("READY"),
    ].map(isRunning),
  ).toEqual([false, false, true, true, false, false, false]);
});
it("to do holds only orders no agent has picked up yet", () => {
  expect(
    [
      withState("CREATED"),
      withState("QUEUED"),
      withState("PLANNING"),
      withState("IMPLEMENTING"),
      withState("BLOCKED"),
      withState("CANCELLED"),
    ].map(isTodo),
  ).toEqual([true, true, false, false, false, false]);
});
// 2026-11-01 is a Sunday
const days = Array.from({ length: 10 }, (_, i) => ({
  date: `2026-11-${String(i + 1).padStart(2, "0")}`,
  count: [1, 0, 3, 0, 0, 0, 0, 2, 0, 4][i],
}));
it("buckets daily counts into weekday rows and week columns", () => {
  const { cells, weeks, months } = heatCells(days, "Daily");
  expect(weeks).toBe(2);
  expect(cells[0]).toMatchObject({ row: 0, col: 0, value: 1 });
  expect(cells[7]).toMatchObject({ row: 0, col: 1, value: 2 });
  expect(cells[9].level).toBe(4);
  expect(cells[1].level).toBe(0);
  expect(months).toEqual([{ col: 0, label: "Nov" }]);
});
it("sums each week column in weekly mode", () => {
  const { cells } = heatCells(days, "Weekly");
  expect(cells.slice(0, 7).map((c) => c.value)).toEqual(Array(7).fill(4));
  expect(cells.slice(7).map((c) => c.value)).toEqual([6, 6, 6]);
  expect(cells[0].label).toContain("Week of Nov 1");
});
it("accumulates in cumulative mode and offsets a mid-week start", () => {
  const { cells } = heatCells(days, "Cumulative");
  expect(cells.map((c) => c.value).at(-1)).toBe(10);
  const mid = heatCells(days.slice(2), "Daily");
  expect(mid.cells[0]).toMatchObject({ row: 2, col: 0 });
  expect(heatCells([], "Daily")).toMatchObject({ cells: [], weeks: 0 });
});
