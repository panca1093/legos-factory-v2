import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ChangesPanel from "../../src/features/factory/reviews/ChangesPanel.vue";
import NowCard from "../../src/features/factory/work-orders/NowCard.vue";
import ActivityPanel from "../../src/features/factory/runs/ActivityPanel.vue";
import { diffFromApi } from "../../src/features/factory/reviews/diff";
import { currentReason } from "../../src/features/factory/runs/agentActivity";
import { publishNote } from "../../src/composables/presentation";
import { useFactory } from "../../src/stores/factory";
import type { WODiff, WOEvent, WorkOrder } from "../../src/types/api";
import { orders, runs } from "../fixtures/workspace";

let id = 0;
const ev = (
  kind: string,
  message = kind,
  payload?: Record<string, unknown>,
): WOEvent => ({
  id: ++id,
  work_order_id: "LF-1",
  actor: "factory",
  kind,
  message,
  payload,
  created_at: "2026-11-06T00:00:00Z",
});
const into = (to: string) => ev("state_changed", to, { to });
const wo = (o: Partial<WorkOrder>): WorkOrder => ({ ...orders[0], ...o });

const body = "@@ -1,2 +1,2 @@\n a\n-b\n+c\n";
const preamble =
  "diff --git a/src/b.go b/src/b.go\n--- a/src/b.go\n+++ b/src/b.go\n";
const file = (over: Partial<WODiff["files"][number]>) => ({
  path: "src/a.go",
  status: "modified" as const,
  additions: 1,
  deletions: 1,
  binary: false,
  patch: body,
  truncated: false,
  ...over,
});
const diff: WODiff = {
  available: true,
  base: "1234567890",
  head: "abcdef0123",
  revision: 2,
  files: [
    file({}),
    file({ path: "src/b.go", patch: preamble + body, truncated: true }),
    file({ path: "go.sum", additions: 4, deletions: 0 }),
    file({
      path: "logo.png",
      status: "added",
      additions: 0,
      deletions: 0,
      binary: true,
      patch: "",
    }),
  ],
};
const panel = (props: Record<string, unknown>) =>
  mount(ChangesPanel, {
    props: {
      runs,
      state: "NEEDS_HUMAN_REVIEW",
      canWrite: true,
      busy: false,
      ...props,
    },
  });

describe("diffFromApi", () => {
  it("parses patches with and without a file header, flags lockfiles and binaries", () => {
    const files = diffFromApi(diff);
    expect(files[0].hunks[0].lines.map((l) => l.t)).toEqual([
      "ctx",
      "del",
      "add",
    ]);
    expect(files[1].hunks).toHaveLength(1);
    expect(files[1].truncated).toBe(true);
    expect(files[2].lockfile).toBe(true);
    expect(files[3]).toMatchObject({ binary: true, hunks: [] });
  });
});

describe("Changes panel with the diff endpoint", () => {
  it("renders summary, files and notices, and hides builder output", () => {
    const w = panel({ diff });
    expect(w.find('[data-test="diff-total"]').text()).toContain(
      "4 files changed",
    );
    const range = w.find('[data-test="diff-range"]').text();
    expect(range).toContain("1234567…abcdef0");
    expect(range).toContain("revision 2");
    expect(w.text()).toContain("Binary file not shown.");
    expect(w.text()).toContain("truncated");
    expect(w.find('[aria-label="Expand go.sum"]').exists()).toBe(true);
    expect(w.text()).not.toContain("Builder output");
  });
  it("shows the reason and keeps builder output when unavailable", () => {
    const w = panel({
      diff: { available: false, reason: "Branch not pushed", files: [] },
    });
    expect(w.find('[data-test="diff-unavailable"]').text()).toBe(
      "Branch not pushed",
    );
    expect(w.text()).toContain("Builder output");
  });
  it("states the publish path from the project", () => {
    const w = panel({
      ship: { key: "approval", label: "Awaiting approval", tone: "" },
      publishNote: publishNote({ publish_via: "gh", github_repo: "acme/repo" })
        .text,
    });
    expect(w.text()).toContain(
      "opens a pull request on GitHub (acme/repo) with your gh login.",
    );
    expect(w.text()).not.toContain("No pull request is created");
  });
});

describe("publishNote", () => {
  it("follows publish_via", () => {
    expect(
      publishNote({ publish_via: "gh", github_repo: "a/b" }),
    ).toMatchObject({
      remote: true,
      text: "Approving pushes the branch and opens a pull request on GitHub (a/b) with your gh login.",
    });
    expect(
      publishNote({ publish_via: "connection", remote_repo: "a/b" }).text,
    ).toContain("on a/b through the project's connection");
    expect(
      publishNote({ git_connection_id: "c1", remote_repo: "a/b" }).remote,
    ).toBe(true);
    expect(publishNote({ publish_via: "none" })).toMatchObject({
      remote: false,
    });
    expect(publishNote(undefined).text).toContain("No pull request is created");
  });
});

describe("currentReason", () => {
  const review = wo({ state: "NEEDS_HUMAN_REVIEW" });
  it("ignores a stale plan escalation once in NEEDS_HUMAN_REVIEW", () => {
    expect(
      currentReason(review, [
        into("ARCHITECT_REVIEW"),
        ev("plan_escalated", "old plan reason"),
        into("QUEUED"),
        into("IMPLEMENTING"),
        into("NEEDS_HUMAN_REVIEW"),
        ev("readiness_needs_human_review", "Done gate wants a human"),
      ]),
    ).toBe("Done gate wants a human");
  });
  it("accepts the causing event logged just before the transition", () => {
    expect(
      currentReason(review, [
        into("IMPLEMENTING"),
        ev("done_gate", "x", { outcome: "escalate", note: "note" }),
        into("NEEDS_HUMAN_REVIEW"),
      ]),
    ).toBe("note");
  });
  it("returns nothing when only older-state escalations exist", () => {
    expect(
      currentReason(review, [
        ev("plan_escalated", "old"),
        into("IMPLEMENTING"),
        into("NEEDS_HUMAN_REVIEW"),
      ]),
    ).toBe("");
  });
  it("uses the latest plan escalation for an approved plan", () => {
    const events = [
      ev("plan_escalated", "first"),
      into("ARCHITECT_REVIEW"),
      ev("plan_escalated", "second"),
    ];
    expect(
      currentReason(
        wo({ state: "ARCHITECT_REVIEW", plan_review: "approved" }),
        events,
      ),
    ).toBe("second");
    expect(
      currentReason(
        wo({ state: "ARCHITECT_REVIEW", plan_review: "pending" }),
        events,
      ),
    ).toBe("");
  });
  it("covers waiting, blocked and stalled PR", () => {
    expect(
      currentReason(wo({ state: "WAITING_FOR_HUMAN" }), [
        ev("clarification_requested", "q1"),
        into("WAITING_FOR_HUMAN"),
        ev("clarification_requested", "q2"),
      ]),
    ).toBe("q2");
    expect(
      currentReason(wo({ state: "BLOCKED" }), [
        into("BLOCKED"),
        ev("readiness_blocked", "tests red"),
      ]),
    ).toBe("tests red");
    expect(
      currentReason(
        wo({ state: "READY", pr_closed_at: "t", pr_merged_at: null }),
        [into("READY"), ev("pr_closed", "closed")],
      ),
    ).toBe("closed");
    expect(
      currentReason(wo({ state: "READY", pr_track_failed_at: "t" }), [
        ev("pr_track_failed", "cannot track"),
      ]),
    ).toBe("cannot track");
    expect(
      currentReason(wo({ state: "IMPLEMENTING" }), [ev("plan_escalated", "x")]),
    ).toBe("");
  });
});

describe("NowCard", () => {
  const mountNow = (order: WorkOrder, events: WOEvent[]) =>
    mount(NowCard, {
      props: {
        order,
        tasks: [],
        runs: [],
        events,
        canWrite: true,
        canClarify: true,
        busy: false,
      },
    });
  it("shows the current reason, not a stale plan escalation", () => {
    const w = mountNow(wo({ state: "NEEDS_HUMAN_REVIEW" }), [
      ev("plan_escalated", "old plan reason"),
      into("NEEDS_HUMAN_REVIEW"),
      ev("readiness_needs_human_review", "fresh reason"),
    ]);
    expect(w.text()).toContain("fresh reason");
    expect(w.text()).not.toContain("old plan reason");
  });
  it("Latest uses the newest event by id", () => {
    const w = mountNow(wo({ state: "IMPLEMENTING" }), [
      { ...ev("agent_step", "newest step"), id: 9000 },
      { ...ev("agent_step", "older"), id: 5 },
    ]);
    expect(w.find(".fx-last").text()).toBe("Latest: newest step");
  });
});

describe("activity truncation", () => {
  it("shows the earlier-events note only when has_more", () => {
    const props = { events: [ev("agent_step", "s")], runs: [] };
    expect(
      mount(ActivityPanel, { props })
        .find('[data-test="activity-more"]')
        .exists(),
    ).toBe(false);
    const w = mount(ActivityPanel, { props: { ...props, hasMore: true } });
    expect(w.find('[data-test="activity-more"]').text()).toBe(
      "Earlier events are not shown.",
    );
  });
});

describe("store", () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockReset();
  });
  const json = (data: unknown) => ({
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify(data)),
  });
  it("keeps has_more from /activity and loads /diff", async () => {
    fetchMock.mockImplementation((url: string) => {
      if (url.endsWith("/activity"))
        return Promise.resolve(json({ events: [ev("x")], has_more: true }));
      if (url.endsWith("/diff")) return Promise.resolve(json(diff));
      if (url.endsWith("/quality"))
        return Promise.resolve(json({ checks: [] }));
      if (url.endsWith("/findings"))
        return Promise.resolve(json({ findings: [] }));
      return Promise.resolve(
        json({ work_order: orders[0], plan: [], runs: [] }),
      );
    });
    const f = useFactory();
    await f.loadDetail("LF-1");
    expect(f.eventsHasMore).toBe(true);
    await f.loadDiff("LF-1");
    expect(f.diff?.files).toHaveLength(4);
  });
  it("records an unavailable diff when the request fails", async () => {
    fetchMock.mockResolvedValue({
      ok: false,
      status: 404,
      text: () => Promise.resolve("{}"),
    });
    const f = useFactory();
    await f.loadDiff("LF-1");
    expect(f.diff?.available).toBe(false);
  });
});
