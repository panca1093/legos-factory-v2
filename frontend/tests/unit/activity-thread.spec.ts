import { describe, expect, it } from "vitest";
import {
  activityThread,
  handoffs,
} from "../../src/features/factory/runs/activityThread";
import type { Task, WOEvent } from "../../src/types/api";
import { orders, tasks } from "../fixtures/workspace";

let id = 0;
const ev = (
  actor: string,
  kind: string,
  message = kind,
  payload?: Record<string, unknown>,
): WOEvent => ({
  id: ++id,
  work_order_id: "wo1",
  actor,
  kind,
  message,
  payload,
  created_at: new Date(Date.UTC(2026, 9, 24, 17, 0, id)).toISOString(),
});
const order = {
  ...structuredClone(orders[0]!),
  spec: "the spec",
  clarif_answer: "The user id",
};
const plan: Task[] = [
  { ...tasks[0]!, id: "t1", title: "Add the greeter" },
  { ...tasks[0]!, id: "t2", title: "Wire it up" },
];
const thread = (events: WOEvent[], runs = []) =>
  activityThread(events, order, plan, runs);
const msgs = (events: WOEvent[]) =>
  thread(events).filter((i) => i.kind !== "divider");

describe("activityThread", () => {
  it("opens with the request from You to Foreman and a Brief divider", () => {
    const t = thread([]);
    expect(t[0]).toMatchObject({ kind: "divider", text: "Brief" });
    expect(t[1]).toMatchObject({
      from: "you",
      to: "foreman",
      type: "Request",
    });
  });

  it("keeps the decision on the request when no answer event is loaded", () => {
    expect(thread([])[1]!.docs!.map((d) => d.section)).toEqual([
      "outcome",
      "decision",
    ]);
  });

  it("maps brief_ready and brief_amended to a Foreman task with the brief doc", () => {
    const [, b, a] = msgs([
      ev("foreman", "brief_ready", "ready", { brief: "Do X" }),
      ev("foreman", "brief_amended", "amended", { brief: "Do X and Y" }),
    ]);
    expect(b).toMatchObject({ from: "foreman", to: "architect", type: "Task" });
    expect(b!.docs).toEqual([
      { title: "Foreman's brief", section: "brief", text: "Do X" },
    ]);
    expect(a!.docs![0]!.text).toBe("Do X and Y");
  });

  it("maps plan_ready to a Spec with the four doc cards", () => {
    const [, p] = msgs([
      ev("architect", "plan_ready", "Plan with 2 tasks", {
        summary: "Two steps",
      }),
    ]);
    expect(p).toMatchObject({
      from: "architect",
      to: "foreman",
      type: "Spec",
      text: "Two steps",
    });
    expect(p!.docs!.map((d) => d.section)).toEqual([
      "acceptance",
      "spec",
      "tasks",
      "scope",
    ]);
  });

  it("attaches doc cards only to the latest plan_ready", () => {
    const [, p1, p2] = msgs([
      ev("architect", "plan_ready", "one"),
      ev("architect", "plan_ready", "two"),
    ]);
    expect(p1!.docs).toBeUndefined();
    expect(p2!.docs).toHaveLength(4);
  });

  it("maps plan_reviewed findings to Foreman feedback rows", () => {
    const [, f] = msgs([
      ev("foreman", "plan_reviewed", "notes", {
        findings: [
          { severity: "critical", issue: "No rollback", fix: "Add one" },
        ],
      }),
    ]);
    expect(f).toMatchObject({
      from: "foreman",
      to: "architect",
      type: "Feedback",
    });
    expect(f!.findings).toEqual([
      { severity: "critical", title: "No rollback", detail: "Add one" },
    ]);
  });

  it("maps jev gates: revise and escalate are feedback, approve is a system line", () => {
    const [, plan1, conv, ok] = msgs([
      ev("jev", "plan_gate", "g", {
        gate: "plan",
        outcome: "revise",
        note: "Fix it",
        issues: ["a"],
      }),
      ev("jev", "converge_gate", "g", {
        gate: "converge",
        outcome: "escalate",
        issues: ["b"],
      }),
      ev("jev", "plan_gate", "g", { gate: "plan", outcome: "approve" }),
    ]);
    expect(plan1).toMatchObject({
      from: "jev",
      to: "architect",
      type: "Feedback",
      text: "Fix it",
    });
    expect(plan1!.findings).toEqual([{ severity: "major", title: "a" }]);
    expect(conv).toMatchObject({ to: "builder" });
    expect(conv!.pill).toBeUndefined();
    expect(ok).toMatchObject({
      kind: "system",
      pill: { label: "Gate passed" },
    });
  });

  it("threads a revise loop: gate revise, revision request, plan_ready replies quoting the feedback", () => {
    const t = msgs([
      ev("jev", "plan_gate", "g", {
        gate: "plan",
        outcome: "revise",
        issues: ["Missing tests"],
      }),
      ev("factory", "plan_revision_requested", "note", { revision: 1 }),
      ev("architect", "plan_ready", "Plan", { summary: "Revised" }),
    ]);
    expect(t[2]).toMatchObject({
      kind: "system",
      text: "Revision 1 requested",
    });
    expect(t[3]).toMatchObject({
      type: "Reply · revision 1",
      from: "architect",
      quotes: ["Missing tests"],
    });
  });

  it("quotes only critical/high contract findings and resets on a human revision", () => {
    const reviewed = ev("foreman", "plan_reviewed", "n", {
      findings: [
        { severity: "high", layer: "contract", issue: "Keep" },
        { severity: "high", layer: "design", issue: "Wrong layer" },
        { severity: "low", layer: "contract", issue: "Too minor" },
      ],
    });
    const t = msgs([
      reviewed,
      ev("factory", "plan_revision_requested", "n", { revision: 1 }),
      ev("architect", "plan_ready", "p"),
      ev("foreman", "plan_reviewed", "n", {
        findings: [{ severity: "critical", layer: "contract", issue: "Old" }],
      }),
      ev("human", "plan_revision_human", "Do it my way\nmore", {
        revision: 2,
      }),
      ev("architect", "plan_ready", "p2"),
    ]);
    expect(t[3]!.quotes).toEqual(["Keep"]);
    expect(t.at(-1)!.quotes).toEqual(["Do it my way"]);
  });

  it("maps human revisions to the Architect or Builder on the right side", () => {
    const [, a, b] = msgs([
      ev("human", "plan_revision_human", "Rework the plan"),
      ev("human", "revision_requested", "Fix the typo"),
    ]);
    expect(a).toMatchObject({ from: "you", to: "architect", type: "Feedback" });
    expect(b).toMatchObject({ from: "you", to: "builder", type: "Feedback" });
  });

  it("maps clarification questions and answers", () => {
    const [, q, r] = msgs([
      ev("builder", "clarification_requested", "Which key?"),
      ev("human", "clarification_answered", "The user id", {
        question: "Which key?",
      }),
    ]);
    expect(q).toMatchObject({ from: "builder", to: "you", type: "Question" });
    expect(r).toMatchObject({
      from: "you",
      to: "builder",
      type: "Reply",
      quotes: ["Which key?"],
    });
    expect(r!.docs).toEqual([
      { title: "Decision recorded", section: "decision" },
    ]);
  });

  it("sends an agent's clarification_asked to the Foreman and the Foreman's answer back", () => {
    const [, q, a] = msgs([
      ev("architect", "clarification_asked", "Which key?"),
      ev("foreman", "clarification_answered", "The id", {
        question: "Which key?",
      }),
    ]);
    expect(q).toMatchObject({
      from: "architect",
      to: "foreman",
      type: "Question",
    });
    expect(a).toMatchObject({
      from: "foreman",
      to: "architect",
      type: "Reply",
    });
    expect(a!.docs).toBeUndefined();
  });

  it("renders a repeated answer as a system line and puts the decision doc on the last human answer only", () => {
    const t = msgs([
      ev("architect", "clarification_requested", "Q1"),
      ev("human", "clarification_answered", "A1", { question: "Q1" }),
      ev("architect", "clarification_requested", "Q2"),
      ev("human", "clarification_answered", "A2", { question: "Q2" }),
      ev("foreman", "clarification_answered", "A1", {
        question: "Q1",
        repeated: true,
      }),
    ]);
    expect(t[2]!.docs).toBeUndefined();
    expect(t[4]!.docs).toEqual([
      { title: "Decision recorded", section: "decision" },
    ]);
    expect(t[5]).toMatchObject({ kind: "system", from: "foreman" });
  });

  it("maps a steer to the run's role, from You", () => {
    const [, s] = msgs([
      ev("architect", "agent_started", "start", { run_id: "r8" }),
      ev("human", "agent_steered", "Use sqlite", { run_id: "r8" }),
    ]);
    expect(s).toMatchObject({ from: "you", to: "architect", type: "Steer" });
  });

  it("maps a builder start with a task to a Foreman task message", () => {
    const [, t, none] = msgs([
      ev("builder", "agent_started", "s", { run_id: "r1", task_id: "t2" }),
      ev("builder", "agent_started", "s", { run_id: "r2" }),
    ]);
    expect(t).toMatchObject({
      from: "foreman",
      to: "builder",
      type: "Task #02",
      text: "Wire it up",
    });
    expect(none).toBeUndefined();
  });

  it("announces a builder task once per task id", () => {
    const t = msgs([
      ev("builder", "agent_started", "s", { run_id: "r1", task_id: "t1" }),
      ev("builder", "agent_started", "s", { run_id: "r2", task_id: "t1" }),
      ev("builder", "agent_started", "s", { run_id: "r3", task_id: "t2" }),
    ]);
    expect(t.map((i) => i.type)).toEqual(["Request", "Task #01", "Task #02"]);
  });

  it("keeps the findings group open across interleaved events until inspection_finished", () => {
    const t = msgs([
      ev("inspector", "inspection_specialists", "go"),
      ev("inspector", "finding", "[sec/blocker] A", { severity: "blocker" }),
      ev("inspector", "agent_started", "s", { run_id: "x" }),
      ev("architect", "agent_completed", "done"),
      ev("inspector", "finding", "[perf/minor] B", { severity: "minor" }),
      ev("inspector", "inspection_finished", "2"),
      ev("factory", "fix_requested", "fix", { revision: 1 }),
    ]);
    expect(t).toHaveLength(3);
    expect(t[1]!.findings!.map((f) => f.title)).toEqual(["A", "B"]);
    expect(t[2]!.quotes).toEqual(["A", "B"]);
  });

  it("flushes checks before a stage change so they land before the next divider", () => {
    const t = thread([
      ev("inspector", "check_pass", "p"),
      ev("factory", "state_changed", "s", { to: "VERIFYING" }),
      ev("inspector", "check_pass", "p"),
    ]);
    expect(t.map((i) => i.type)).toEqual([
      "Stage",
      "Request",
      "Checks",
      "Stage",
      "Checks",
    ]);
  });

  it("does not double count a baseline failure", () => {
    const t = msgs([
      ev("inspector", "check_pass", "a"),
      ev("inspector", "check_fail", "b"),
      ev("inspector", "check_failed_before_change", "b"),
    ]);
    expect(t[1]!.text).toBe("1 passed, 1 failed before the change");
    expect(t[1]!.pill!.tone).toBe("green");
  });

  it("groups inspector findings until inspection_finished", () => {
    const t = msgs([
      ev("inspector", "finding", "[sec/blocker] SQL injection", {
        severity: "blocker",
        location: "a.go:3",
      }),
      ev("inspector", "finding", "[perf/minor] Slow loop", {
        severity: "minor",
      }),
      ev("inspector", "inspection_finished", "2 findings"),
    ]);
    expect(t).toHaveLength(2);
    expect(t[1]).toMatchObject({
      from: "inspector",
      to: "builder",
      type: "Feedback",
    });
    expect(t[1]!.findings).toEqual([
      { severity: "blocker", title: "SQL injection", detail: "a.go:3" },
      { severity: "minor", title: "Slow loop", detail: undefined },
    ]);
  });

  it("maps fix_requested to a Revision N fix task quoting the findings", () => {
    const [, f] = msgs([
      ev("factory", "fix_requested", "Revision 2", {
        revision: 2,
        items: ["[blocker] SQL injection (a.go)"],
      }),
    ]);
    expect(f).toMatchObject({
      from: "foreman",
      to: "builder",
      type: 'Task "Revision 2 fix"',
      quotes: ["[blocker] SQL injection (a.go)"],
    });
  });

  it("turns progress events into system lines and folds checks into one", () => {
    const t = msgs([
      ev("factory", "commit_recorded", "Committed abc"),
      ev("builder", "task_merged", "Task t1 merged"),
      ev("inspector", "check_pass", "lint → PASS"),
      ev("inspector", "check_pass", "test → PASS"),
      ev("inspector", "check_fail", "e2e → FAIL"),
      ev("inspector", "verification_passed", "ok"),
      ev("factory", "pr_opened", "PR #1"),
      ev("factory", "escalated", "Blockers remain"),
      ev("human", "paused", "p"),
      ev("human", "resumed", "r"),
      ev("human", "cancelled", "c"),
    ]).slice(1);
    expect(t.every((i) => i.kind === "system")).toBe(true);
    expect(t.map((i) => i.pill!.label)).toEqual([
      "Committed",
      "Merged",
      "Checks",
      "Verified",
      "PR opened",
      "Needs you",
      "Paused",
      "Resumed",
      "Cancelled",
    ]);
    expect(t[2]!.text).toBe("2 passed, 1 failed");
    expect(t[2]!.pill!.tone).toBe("red");
    expect(t[5]!.text).toBe("Blockers remain");
  });

  it("puts a stage divider at the first item of each later stage, once", () => {
    const t = thread([
      ev("architect", "plan_ready", "p"),
      ev("factory", "state_changed", "s", { to: "IMPLEMENTING" }),
      ev("builder", "task_merged", "m"),
      ev("factory", "state_changed", "s", { to: "VERIFYING" }),
      ev("builder", "task_merged", "m2"),
    ]).filter((i) => i.kind === "divider");
    expect(t.map((d) => d.text)).toEqual(["Brief", "Build"]);
  });

  it("sorts out-of-order events by id and drops unknown kinds", () => {
    const a = ev("factory", "task_merged", "first");
    const b = ev("factory", "commit_recorded", "second");
    const t = msgs([
      b,
      ev("factory", "mystery_kind", "x"),
      a,
      ev("x", "state_changed", "s"),
    ]);
    expect(t.slice(1).map((i) => i.text)).toEqual(["first", "second"]);
  });

  it("survives missing and malformed payloads", () => {
    const t = msgs([
      ev("jev", "plan_gate", "g"),
      ev("foreman", "plan_reviewed", "n", { findings: "nope" }),
      ev("foreman", "brief_ready", "b", { brief: 5 }),
      ev("architect", "plan_ready", "p", { summary: null }),
    ]);
    expect(t.length).toBeGreaterThan(1);
  });

  it("counts handoffs by pair", () => {
    const items = thread([
      ev("foreman", "plan_reviewed", "n", { findings: [{ issue: "x" }] }),
      ev("builder", "agent_started", "s", { task_id: "t1" }),
      ev("foreman", "plan_reviewed", "n", { findings: [] }),
    ]);
    expect(handoffs(items)).toEqual([
      { from: "foreman", to: "architect", tasks: 0, feedback: 2 },
      { from: "foreman", to: "builder", tasks: 1, feedback: 0 },
    ]);
  });
});
