import { mount, flushPromises, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../src/features/projects/pulls/highlight", async (orig) => ({
  ...(await orig<object>()),
  highlightHunks: async () => null,
}));
import ProjectRail from "../../src/features/projects/ProjectRail.vue";
import PullsView from "../../src/features/projects/pulls/PullsView.vue";
import PRView from "../../src/features/projects/pulls/PRView.vue";
import PRPanel from "../../src/features/projects/pulls/PRPanel.vue";
import ReviewsView from "../../src/features/projects/ReviewsView.vue";
import { fileTree, parsePatch } from "../../src/features/projects/pulls/diff";
import { segments } from "../../src/features/projects/pulls/highlight";
import { useFactory } from "../../src/stores/factory";
import {
  chipText,
  prFilter,
  statusLine,
  type PRDraft,
  type PRFile,
  type PRRow,
} from "../../src/types/review";

const PATCH = [
  "--- a/a.go",
  "+++ b/a.go",
  "@@ -1,3 +1,4 @@",
  " package a",
  "-old",
  "+new",
  "+extra",
  " end",
  "",
].join("\n");

const file = (
  path: string,
  patch = PATCH,
  over: Partial<PRFile> = {},
): PRFile => ({
  path,
  old_path: path,
  status: "modified",
  additions: 2,
  deletions: 1,
  patch,
  truncated: false,
  viewed: false,
  ...over,
});
const draft = (id: string, over: Partial<PRDraft> = {}): PRDraft => ({
  id,
  run_id: "r1",
  path: "a.go",
  old_path: "a.go",
  side: "new",
  line: 2,
  old_line: 0,
  inline: true,
  severity: "major",
  title: `Title ${id}`,
  body: `Body ${id}`,
  ai_body: `Body ${id}`,
  status: "pending",
  posted_ref: "",
  post_error: "",
  ...over,
});
const prow = (n: number, over: Partial<PRRow> = {}): PRRow => ({
  number: n,
  title: `PR ${n}`,
  url: `https://github.com/o/r/pull/${n}`,
  author: { login: "ann", id: "1" },
  head: "feat",
  base: "main",
  draft: false,
  updated_at: "2026-11-19T00:00:00Z",
  role: "reviewer",
  label: "new",
  pending_drafts: 0,
  running: false,
  ...over,
});
const identity = {
  provider: "github",
  host: "github.com",
  source: "gh",
  login: "me",
  readiness: "ready",
};
const detailBody = (over: Record<string, unknown> = {}) => ({
  pr: {
    number: 7,
    title: "Retry jobs",
    url: "https://github.com/o/r/pull/7",
    author: { login: "ann", id: "1" },
    head: "feat",
    base: "main",
    head_sha: "abc1234",
    base_sha: "def",
    draft: false,
    updated_at: "2026-11-19T00:00:00Z",
    body: "Body <script>alert(1)</script>",
    additions: 2,
    deletions: 1,
    changed_files: 1,
    ci: "passing",
    source_repo: "o/r",
  },
  identity,
  review: null,
  run: null,
  verdicts: {
    approve: { ok: true },
    request_changes: { ok: false, reason: "Not for your own PR" },
  },
  ai: { ok: true },
  ...over,
});

type Call = { method: string; url: string; body?: unknown };
let calls: Call[];
let routes: Record<string, (c: Call) => unknown>;
function stubFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const c: Call = {
        method: init?.method ?? "GET",
        url,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      calls.push(c);
      const key = Object.keys(routes).find((k) => {
        const [m, ...u] = k.split(" ");
        return m === c.method && url.includes(u.join(" "));
      });
      const out = key ? routes[key](c) : {};
      const status =
        out && typeof out === "object" && "__status" in out
          ? (out as { __status: number }).__status
          : 200;
      const body =
        out && typeof out === "object" && "__body" in out
          ? (out as { __body: unknown }).__body
          : out;
      return {
        ok: status < 400,
        status,
        text: async () => JSON.stringify(body),
      };
    }),
  );
}
const count = (method: string, frag: string) =>
  calls.filter((c) => c.method === method && c.url.includes(frag)).length;

function makeRouter() {
  const blank = { render: () => null };
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/projects", component: blank },
      { path: "/settings", component: blank },
      { path: "/reviews", component: blank },
      {
        path: "/projects/:id",
        component: blank,
        children: [
          { path: "", name: "project", component: blank },
          { path: "pulls", name: "project-pulls", component: blank },
          { path: "pulls/:number", name: "project-pull", component: blank },
        ],
      },
    ],
  });
}

beforeEach(() => {
  setActivePinia(createPinia());
  calls = [];
  routes = {};
  stubFetch();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("pure helpers", () => {
  it("parses hunks with line numbers", () => {
    const h = parsePatch(PATCH);
    expect(h).toHaveLength(1);
    expect(h[0].lines.map((l) => [l.t, l.o, l.n, l.s])).toEqual([
      ["ctx", 1, 1, "package a"],
      ["del", 2, undefined, "old"],
      ["add", undefined, 2, "new"],
      ["add", undefined, 3, "extra"],
      ["ctx", 3, 4, "end"],
    ]);
    expect(parsePatch("")).toEqual([]);
  });
  it("compacts single-child folder chains and honours closed dirs", () => {
    const files = [
      file("internal/adapter/x.go"),
      file("internal/adapter/y.go"),
      file("README.md"),
    ];
    const rows = fileTree(files, new Set());
    expect(rows.map((r) => `${r.kind}:${r.depth}:${r.name}`)).toEqual([
      "dir:0:internal/adapter",
      "file:1:x.go",
      "file:1:y.go",
      "file:0:README.md",
    ]);
    expect(fileTree(files, new Set(["internal/adapter"]))).toHaveLength(2);
  });
  it("splits a line into token segments by kind", () => {
    expect(
      segments('func x() "s"', [
        { offset: 0, type: "keyword.go" },
        { offset: 4, type: "white.go" },
        { offset: 5, type: "identifier.go" },
        { offset: 6, type: "delimiter.parenthesis.go" },
        { offset: 8, type: "white.go" },
        { offset: 9, type: "string.go" },
      ]),
    ).toEqual([
      { t: "func", c: "tk-keyword" },
      { t: " ", c: "" },
      { t: "x", c: "" },
      { t: "()", c: "" },
      { t: " ", c: "" },
      { t: '"s"', c: "tk-string" },
    ]);
    expect(segments("plain", [])).toEqual([{ t: "plain", c: "" }]);
  });

  it("maps labels and filters", () => {
    const r = { label: "ai" as const, pending_drafts: 3, running: false };
    expect(chipText(r)).toBe("AI · 3");
    expect(chipText({ ...r, running: true })).toBe("AI · …");
    expect(chipText({ ...r, label: "none" })).toBe("");
    expect(chipText({ ...r, label: "yours" })).toBe("yours");
    expect(statusLine(prow(1, { label: "approve" }))).toBe(
      "Needs your approval",
    );
    expect(prFilter("reviewer")).toBe("reviewer");
    expect(prFilter("zzz")).toBe("related");
  });
});

describe("rail pull requests section", () => {
  let w: VueWrapper;
  async function mountRail(pulls: unknown, path = "/projects/p1") {
    routes["GET /projects/p1/rail?part=pulls"] = () => ({
      project: { id: "p1", name: "P", repo: "r", needs_you: 0 },
      pulls,
    });
    routes["GET /projects/p1/rail"] = () => ({
      project: { id: "p1", name: "P", repo: "r", needs_you: 0 },
      work_orders: { open: 0, rows: [] },
      pulls,
    });
    const router = makeRouter();
    await router.push(path);
    await router.isReady();
    w = mount(ProjectRail, {
      global: { plugins: [router], stubs: { UsageCard: true } },
    });
    await flushPromises();
    return router;
  }
  afterEach(() => w?.unmount());

  it("shows rows with labels, count and the foot link", async () => {
    await mountRail({
      identity,
      count: 3,
      stale: false,
      rows: [
        {
          number: 5,
          title: "Alpha",
          label: "ai",
          pending_drafts: 4,
          running: false,
        },
        {
          number: 6,
          title: "Beta",
          label: "ai",
          pending_drafts: 0,
          running: true,
        },
        {
          number: 7,
          title: "Gamma",
          label: "none",
          pending_drafts: 0,
          running: false,
        },
      ],
    });
    const rows = w.findAll('[data-test="rail-pr"]');
    expect(rows).toHaveLength(3);
    expect(rows[0].text()).toContain("#5");
    expect(rows[0].text()).toContain("AI · 4");
    expect(rows[1].text()).toContain("AI · …");
    expect(rows[2].classes()).toContain("quiet");
    expect(rows[2].find(".pr-label").exists()).toBe(false);
    expect(rows[0].attributes("href")).toBe("/projects/p1/pulls/5");
    expect(w.find('[data-test="rail-pulls-all"]').attributes("href")).toBe(
      "/projects/p1/pulls",
    );
    expect(w.find('[data-test="rail-pulls-warn"]').exists()).toBe(false);
  });
  it("links to Connections when there is no identity", async () => {
    await mountRail({
      identity: null,
      count: 0,
      stale: false,
      rows: [],
      error: { code: "no_identity", message: "x" },
    });
    const a = w.find('[data-test="rail-connect"]');
    expect(a.text()).toBe("Connect an account");
    expect(a.attributes("href")).toBe("/settings?section=connections");
    expect(w.find('[data-test="rail-pr"]').exists()).toBe(false);
  });
  it("keeps cached rows and shows a warning dot with the reason on rate limit", async () => {
    await mountRail({
      identity,
      count: 1,
      stale: true,
      rows: [
        {
          number: 5,
          title: "Alpha",
          label: "new",
          pending_drafts: 0,
          running: false,
        },
      ],
      error: { code: "rate_limited", message: "Rate limited until 10:00" },
    });
    expect(w.findAll('[data-test="rail-pr"]')).toHaveLength(1);
    expect(w.find('[data-test="rail-pulls-warn"]').attributes("title")).toBe(
      "Rate limited until 10:00",
    );
  });
  it("caps at 8 rows with Show more and refetches on focus and pr_review events", async () => {
    const rows = Array.from({ length: 10 }, (_, i) => ({
      number: i + 1,
      title: `T${i + 1}`,
      label: "new",
      pending_drafts: 0,
      running: false,
    }));
    await mountRail({ identity, count: 10, stale: false, rows });
    expect(w.findAll('[data-test="rail-pr"]')).toHaveLength(8);
    await w.find(".rt-more").trigger("click");
    expect(w.findAll('[data-test="rail-pr"]')).toHaveLength(10);

    const before = count("GET", "part=pulls");
    window.dispatchEvent(new Event("focus"));
    await flushPromises();
    expect(count("GET", "part=pulls")).toBe(before + 1);

    const factory = useFactory();
    factory.liveEvents = [
      {
        id: 1,
        work_order_id: "",
        actor: "",
        kind: "pr_review.run_finished",
        message: "",
        payload: { project_id: "p1", number: 3 },
      },
    ];
    await flushPromises();
    expect(count("GET", "part=pulls")).toBe(before + 2);
    factory.liveEvents = [
      { ...factory.liveEvents[0], id: 2, payload: { project_id: "other" } },
    ];
    await flushPromises();
    expect(count("GET", "part=pulls")).toBe(before + 2);
  });
  it("stops its timers and listeners on unmount", async () => {
    vi.useFakeTimers();
    try {
      await mountRail({ identity, count: 0, stale: false, rows: [] });
      w.unmount();
      const before = count("GET", "part=pulls");
      vi.advanceTimersByTime(180_000);
      window.dispatchEvent(new Event("focus"));
      await flushPromises();
      expect(count("GET", "part=pulls")).toBe(before);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("pull request list", () => {
  it("lists rows with Draft badge and switches filter through the query", async () => {
    routes["GET /projects/p1/pull-requests"] = (c) => ({
      identity,
      stale: false,
      rows: c.url.includes("filter=reviewer")
        ? [prow(2)]
        : [
            prow(1, { draft: true, label: "ai", pending_drafts: 2 }),
            prow(3, { label: "none" }),
          ],
    });
    const router = makeRouter();
    await router.push("/projects/p1/pulls");
    const w = mount(PullsView, { global: { plugins: [router] } });
    await flushPromises();
    const rows = w.findAll('[data-test="pr-row"]');
    expect(rows).toHaveLength(2);
    expect(rows[0].find('[data-test="pr-draft"]').text()).toBe("Draft");
    expect(rows[0].text()).toContain("AI draft ready · 2");
    expect(rows[1].classes()).toContain("muted");
    expect(calls.at(-1)?.url).toContain("filter=related");
    await w.find('[data-test="chip-reviewer"]').trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.filter).toBe("reviewer");
    expect(calls.at(-1)?.url).toContain("filter=reviewer");
    expect(w.findAll('[data-test="pr-row"]')).toHaveLength(1);
    w.unmount();
  });
  it("shows the connect line without identity", async () => {
    routes["GET /projects/p1/pull-requests"] = () => ({
      identity: null,
      stale: false,
      rows: [],
      error: {
        code: "no_identity",
        message: "Connect an account to review github.com",
      },
    });
    const router = makeRouter();
    await router.push("/projects/p1/pulls");
    const w = mount(PullsView, { global: { plugins: [router] } });
    await flushPromises();
    expect(w.find('[data-test="pr-no-identity"]').text()).toContain(
      "Connect an account",
    );
    w.unmount();
  });
});

describe("reviews inbox", () => {
  it("groups by project and reuses the row component", async () => {
    routes["GET /api/v1/reviews"] = () => [
      {
        project: { id: "p1", name: "One" },
        identity,
        rows: [prow(1), prow(2)],
      },
      {
        project: { id: "p2", name: "Two" },
        identity: null,
        rows: [],
        error: {
          code: "no_identity",
          message: "Connect an account to review gitlab.com",
        },
      },
    ];
    const router = makeRouter();
    await router.push("/reviews");
    const w = mount(ReviewsView, { global: { plugins: [router] } });
    await flushPromises();
    const groups = w.findAll('[data-test="review-group"]');
    expect(groups).toHaveLength(2);
    expect(groups[0].findAll('[data-test="pr-row"]')).toHaveLength(2);
    expect(groups[0].find('[data-test="pr-row"]').attributes("href")).toBe(
      "/projects/p1/pulls/1",
    );
    expect(groups[1].text()).toContain(
      "Connect an account to review gitlab.com",
    );
    await w.find('[data-test="chip-author"]').trigger("click");
    await flushPromises();
    expect(calls.at(-1)?.url).toContain("/api/v1/reviews?filter=author");
    w.unmount();
  });
});

describe("pull request selection", () => {
  let drafts: PRDraft[];
  let detail: ReturnType<typeof detailBody>;
  async function mountPR(path = "/projects/p1/pulls/7") {
    routes["GET /pull-requests/7/files"] = () => [
      file("a.go"),
      file("pkg/b.go", PATCH, { viewed: true }),
    ];
    routes["GET /pull-requests/7/review/drafts"] = () => drafts;
    routes["GET /pull-requests/7"] = () => detail;
    routes["PATCH /review/drafts/"] = (c) => {
      const id = c.url.split("/").at(-1)!;
      const patch = c.body as { status?: string; body?: string };
      const d = drafts.find((x) => x.id === id)!;
      Object.assign(d, patch);
      return d;
    };
    const router = makeRouter();
    await router.push(path);
    const w = mount(
      {
        components: { PRView, PRPanel },
        template: "<div><PRView /><PRPanel /></div>",
      },
      { global: { plugins: [router] }, attachTo: document.body },
    );
    await flushPromises();
    return { w, router };
  }
  beforeEach(() => {
    drafts = [
      draft("d1"),
      draft("d2", { line: 99, title: "Loose" }),
      draft("g1", { inline: false, title: "General one" }),
    ];
    detail = detailBody();
  });

  it("renders header, escaped body, diff and inline drafts under their line", async () => {
    const { w } = await mountPR();
    expect(w.find('[data-test="pr-header"]').text()).toContain("Retry jobs");
    expect(w.find('[data-test="pr-header"]').text()).toContain("ann");
    expect(w.find('[data-test="pr-header"]').text()).toContain("feat → main");
    expect(w.find('[data-test="pr-ci"]').text()).toBe("CI passing");
    expect(w.find('[data-test="pr-host-link"]').attributes("rel")).toBe(
      "noopener noreferrer",
    );
    expect(w.find('[data-test="pr-identity"]').text()).toContain("me");
    expect(w.find("script").exists()).toBe(false);
    expect(w.findAll('[data-test="diff-file"]').map((h) => h.text())).toEqual([
      "pkg/b.go",
      "a.go",
    ]);
    const toggles = w.findAll('[data-test="diff-toggle"]');
    expect(toggles[0].attributes("aria-expanded")).toBe("false");
    expect(toggles[1].attributes("aria-expanded")).toBe("true");
    const lines = w.findAll(".dv-line");
    expect(lines).toHaveLength(5);
    const slot = w.find(".dv-slot");
    expect(slot.exists()).toBe(true);
    expect(slot.text()).toContain("Title d1");
    expect(slot.element.previousElementSibling?.textContent).toContain("new");
    expect(w.find('[data-test="pr-general"]').text()).toContain("General one");
    expect(w.find(".prv-loose").text()).toContain("Loose");
    w.unmount();
  });

  it("lists findings; a finding opens its (collapsed) file and scrolls to its draft", async () => {
    drafts = [
      draft("b1", { path: "pkg/b.go", severity: "minor", title: "Minor on b" }),
      draft("b2", {
        path: "pkg/b.go",
        severity: "blocker",
        title: "Blocker on b",
      }),
      draft("g1", { inline: false, title: "General one", severity: "info" }),
    ];
    const { w, router } = await mountPR();
    expect(w.find('[data-test="diff-file"]').text()).toBe("pkg/b.go");
    const items = w.findAll('[data-test^="finding-"]');
    expect(items.map((i) => i.attributes("data-test"))).toEqual([
      "finding-b2",
      "finding-b1",
      "finding-g1",
    ]);
    expect(items[0].text()).toContain("Blocker on b");
    expect(items[0].text()).toContain("b.go:2");
    expect(items[2].text()).toContain("General");
    await items[1].trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toMatchObject({
      file: "pkg/b.go",
      draft: "b1",
    });
    expect(w.find('[data-test="draft-b1"]').classes()).toContain("flash");

    await w.findAll('[data-test="file-pick"]')[0].trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.draft).toBeUndefined();
    await w.find('[data-test="finding-b1"]').trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toMatchObject({
      file: "pkg/b.go",
      draft: "b1",
    });
    w.unmount();
  });

  it("shows every file, viewed ones collapsed; ticking Viewed collapses and unticking reopens", async () => {
    const { w } = await mountPR();
    const section = (p: string) =>
      w
        .findAll('[data-test="diff-section"]')
        .find((x) => x.attributes("data-path") === p)!;
    expect(section("pkg/b.go").find(".dv").exists()).toBe(false);
    await section("pkg/b.go")
      .find('[data-test="diff-toggle"]')
      .trigger("click");
    expect(section("pkg/b.go").find(".dv").exists()).toBe(true);
    routes["PUT /review/viewed"] = () => ({ viewed: 2, total: 2 });
    await section("a.go").find('[data-test="diff-viewed"]').setValue(true);
    await flushPromises();
    expect(section("a.go").find(".dv").exists()).toBe(false);
    expect(section("pkg/b.go").find(".dv").exists()).toBe(true);
    routes["PUT /review/viewed"] = () => ({ viewed: 1, total: 2 });
    await section("a.go").find('[data-test="diff-viewed"]').setValue(false);
    await flushPromises();
    expect(section("a.go").find(".dv").exists()).toBe(true);
    w.unmount();
  });

  it("puts Changes and Review in tabs, Review first while findings wait", async () => {
    const { w, router } = await mountPR();
    const shown = (id: string) =>
      (w.find(`#${id}`).element as HTMLElement).style.display !== "none";
    expect(w.find('[data-test="tab-review"]').attributes("aria-selected")).toBe(
      "true",
    );
    expect(w.find('[data-test="tab-review-badge"]').text()).toBe("3");
    expect(w.find('[data-test="tab-changes-badge"]').text()).toBe("1/2");
    expect(shown("prp-review")).toBe(true);
    expect(shown("prp-changes")).toBe(false);
    await w.find('[data-test="tab-changes"]').trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.tab).toBe("changes");
    expect(shown("prp-changes")).toBe(true);
    expect(shown("prp-review")).toBe(false);
    w.unmount();
  });

  it("keeps the tab chosen at load when the last finding is decided", async () => {
    drafts = [draft("d1")];
    const { w } = await mountPR();
    expect(w.find('[data-test="tab-review"]').attributes("aria-selected")).toBe(
      "true",
    );
    await w
      .find('[data-test="draft-d1"] [data-test="draft-discard"]')
      .trigger("click");
    await flushPromises();
    expect(w.find('[data-test="tab-review-badge"]').exists()).toBe(false);
    expect(w.find('[data-test="tab-review"]').attributes("aria-selected")).toBe(
      "true",
    );
    w.unmount();
  });

  it("a new pick never reopens the file picked before", async () => {
    const { w } = await mountPR("/projects/p1/pulls/7?file=a.go");
    const section = (p: string) =>
      w
        .findAll('[data-test="diff-section"]')
        .find((x) => x.attributes("data-path") === p)!;
    await section("a.go").find('[data-test="diff-toggle"]').trigger("click");
    expect(section("a.go").find(".dv").exists()).toBe(false);
    const picks = w.findAll('[data-test="file-pick"]');
    await picks[0].trigger("click");
    await flushPromises();
    expect(section("pkg/b.go").find(".dv").exists()).toBe(true);
    expect(section("a.go").find(".dv").exists()).toBe(false);
    w.unmount();
  });

  it("selects the file from ?file= and pushes on pick", async () => {
    const { w, router } = await mountPR("/projects/p1/pulls/7?file=pkg%2Fb.go");
    expect(w.find('[data-test="diff-file"]').text()).toBe("pkg/b.go");
    await w.findAll('[data-test="file-pick"]')[1].trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.file).toBe("a.go");
    w.unmount();
  });

  it("accepts, edits, discards and undoes drafts through legal transitions", async () => {
    const { w } = await mountPR();
    const card = () => w.find('[data-test="draft-d1"]');
    await card().find('[data-test="draft-accept"]').trigger("click");
    await flushPromises();
    expect(calls.at(-1)).toMatchObject({
      method: "PATCH",
      body: { status: "accepted" },
    });
    expect(card().find('[data-test="draft-unaccept"]').exists()).toBe(true);
    expect(card().find('[data-test="draft-discard"]').exists()).toBe(false);

    await card().find('[data-test="draft-edit"]').trigger("click");
    await card().find('[data-test="draft-text"]').setValue("Better words");
    await card().find('[data-test="draft-save"]').trigger("click");
    await flushPromises();
    expect(calls.at(-1)?.body).toEqual({ body: "Better words" });
    expect(card().find('[data-test="draft-edited"]').exists()).toBe(true);

    await card().find('[data-test="draft-unaccept"]').trigger("click");
    await flushPromises();
    expect(card().attributes("data-status")).toBe("pending");
    await card().find('[data-test="draft-discard"]').trigger("click");
    await flushPromises();
    expect(card().find('[data-test="draft-undo"]').exists()).toBe(true);
    expect(card().text()).toContain("Discarded");
    await card().find('[data-test="draft-undo"]').trigger("click");
    await flushPromises();
    expect(card().attributes("data-status")).toBe("pending");
    w.unmount();
  });

  it("adds the user's own comment on a removed line, then deletes and restores it", async () => {
    routes["POST /pull-requests/7/review/drafts"] = (c) => {
      const b = c.body as {
        path: string;
        side: "old";
        line: number;
        body: string;
      };
      const d = draft("own1", {
        run_id: "",
        side: b.side,
        line: b.line,
        old_line: b.line,
        body: b.body,
        ai_body: "",
        severity: "info",
        title: "",
        status: "accepted",
      });
      drafts.push(d);
      return d;
    };
    const { w } = await mountPR();
    const removed = w.findAll(".dv-line")[1];
    expect(removed.classes()).toContain("del");
    await removed.find('[data-test="diff-add-comment"]').trigger("click");
    await flushPromises();
    const form = w.find('[data-test="diff-compose"]');
    expect(form.exists()).toBe(true);
    await form.find("textarea").setValue("Why remove this?");
    await form.trigger("submit");
    await flushPromises();
    expect(calls.at(-1)).toMatchObject({
      method: "POST",
      body: { path: "a.go", side: "old", line: 2, body: "Why remove this?" },
    });
    expect(w.find('[data-test="diff-compose"]').exists()).toBe(false);
    const card = () => w.find('[data-test="draft-own1"]');
    expect(card().find('[data-test="draft-own"]').text()).toBe("You");
    expect(
      card()
        .element.closest(".dv-slot")
        ?.previousElementSibling?.classList.contains("del"),
    ).toBe(true);
    expect(card().find('[data-test="draft-accept"]').exists()).toBe(false);

    await card().find('[data-test="draft-delete"]').trigger("click");
    await flushPromises();
    expect(calls.at(-1)?.body).toEqual({ status: "discarded" });
    expect(card().text()).toContain("Deleted");
    await card().find('[data-test="draft-undo"]').trigger("click");
    await flushPromises();
    expect(calls.at(-1)?.body).toEqual({ status: "accepted" });
    expect(card().attributes("data-status")).toBe("accepted");
    w.unmount();
  });

  it("shows outdated and superseded drafts collapsed without actions", async () => {
    drafts = [
      draft("o1", { status: "outdated" }),
      draft("s1", { status: "superseded" }),
    ];
    const { w } = await mountPR();
    const o = w.find('[data-test="draft-o1"]');
    expect(o.text()).toContain("Outdated");
    expect(o.find('[data-test="draft-accept"]').exists()).toBe(false);
    w.unmount();
  });

  it("disables Run AI review with the reason and Re-run after a run", async () => {
    detail = detailBody({
      ai: {
        ok: false,
        reason: "AI review is not available for fork pull requests yet",
      },
    });
    let { w } = await mountPR();
    const run = w.find('[data-test="pr-run"]');
    expect(run.attributes("disabled")).toBeDefined();
    expect(w.find('[data-test="pr-run-reason"]').text()).toContain(
      "fork pull requests",
    );
    w.unmount();

    setActivePinia(createPinia());
    detail = detailBody({
      run: {
        id: "r1",
        head_sha: "abc1234",
        state: "completed",
        summary: "S",
        error: "",
      },
    });
    ({ w } = await mountPR());
    expect(w.find('[data-test="pr-run"]').text()).toBe("Re-run AI review");
    routes["POST /review/runs"] = () => ({
      id: "r2",
      state: "running",
      head_sha: "abc1234",
      summary: "",
      error: "",
    });
    await w.find('[data-test="pr-run"]').trigger("click");
    await flushPromises();
    expect(count("POST", "/review/runs")).toBe(1);
    expect(w.find('[data-test="pr-run"]').attributes("disabled")).toBeDefined();
    w.unmount();
  });

  it("changes panel: tree, viewed checkbox, draft counts", async () => {
    const { w } = await mountPR();
    expect(w.find('[data-test="viewed-count"]').text()).toBe("1 / 2 viewed");
    expect(w.find('[data-test="file-drafts"]').text()).toBe("2");
    routes["PUT /review/viewed"] = () => ({ viewed: 2, total: 2 });
    const boxes = w.findAll('[data-test="file-viewed"]');
    await boxes[1].setValue(true);
    await flushPromises();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({ path: "a.go", viewed: true });
    expect(w.find('[data-test="viewed-count"]').text()).toBe("2 / 2 viewed");
    w.unmount();
  });

  it("review card: disabled verdict with reason, prefilled summary, accepted count", async () => {
    detail = detailBody({
      run: {
        id: "r1",
        head_sha: "abc1234",
        state: "completed",
        summary: "AI says ok",
        error: "",
      },
    });
    drafts = [draft("d1", { status: "accepted" }), draft("d2")];
    const { w } = await mountPR();
    expect(
      (w.find('[data-test="review-summary"]').element as HTMLTextAreaElement)
        .value,
    ).toBe("AI says ok");
    const rc = w.find('[data-test="verdict-request_changes"]');
    expect(rc.attributes("disabled")).toBeDefined();
    expect(w.text()).toContain("Not for your own PR");
    expect(w.find('[data-test="accepted-count"]').text()).toContain(
      "1 accepted comment",
    );
    w.unmount();
  });

  it("posts only after confirmation, with expect, and never automatically", async () => {
    drafts = [
      draft("d1", { status: "accepted" }),
      draft("d2", { status: "accepted", line: 3 }),
    ];
    routes["PATCH /pull-requests/7/review"] = (c) => ({
      id: "rv",
      head_sha: "abc1234",
      summary: "",
      verdict: "comment",
      repo: "o/r",
      posted_head_sha: "",
      posted_verdict: "",
      posted_as: "",
      ...(c.body as object),
    });
    routes["POST /review/post"] = () => ({
      ref: "x",
      comments: { d1: "1", d2: "2" },
      failed: {},
    });
    const { w } = await mountPR();
    expect(count("POST", "/review/post")).toBe(0);
    await w.find('[data-test="verdict-approve"]').setValue(true);
    await flushPromises();
    await w.find('[data-test="post-open"]').trigger("click");
    await flushPromises();
    expect(count("POST", "/review/post")).toBe(0);
    const dlg = document.body.querySelector('[data-test="post-dialog"]')!;
    expect(dlg.textContent).toContain("me");
    expect(dlg.textContent).toContain("github.com");
    expect(dlg.textContent).toContain("#7");
    expect(dlg.textContent).toContain("approve");
    expect(dlg.textContent).toContain("Public on github.com");
    expect(dlg.querySelector('[data-test="post-comments"]')?.textContent).toBe(
      "2",
    );
    (
      document.body.querySelector('[data-test="post-confirm"]') as HTMLElement
    ).click();
    await flushPromises();
    const post = calls.find(
      (c) => c.method === "POST" && c.url.endsWith("/review/post"),
    );
    expect(post?.body).toEqual({
      confirm: true,
      expect: {
        login: "me",
        head_sha: "abc1234",
        comments: 2,
        verdict: "approve",
      },
    });
    expect(w.find('[data-test="post-result"]').text()).toContain(
      "Posted 2 comment",
    );
    w.unmount();
  });

  it("on a 409 shows the current values and posts again only after a second confirmation", async () => {
    drafts = [draft("d1", { status: "accepted" })];
    routes["PATCH /pull-requests/7/review"] = () => ({
      id: "rv",
      head_sha: "abc1234",
      summary: "",
      verdict: "comment",
      repo: "o/r",
      posted_head_sha: "",
      posted_verdict: "",
      posted_as: "",
    });
    let n = 0;
    routes["POST /review/post"] = () =>
      ++n === 1
        ? {
            __status: 409,
            __body: {
              error: "The review changed",
              current: {
                login: "me",
                head_sha: "fff9999",
                comments: 1,
                verdict: "comment",
              },
            },
          }
        : {
            ref: "x",
            comments: { d1: "1" },
            failed: { d9: "boom" },
            verdict_err: "no",
          };
    const { w } = await mountPR();
    await w.find('[data-test="post-open"]').trigger("click");
    await flushPromises();
    (
      document.body.querySelector('[data-test="post-confirm"]') as HTMLElement
    ).click();
    await flushPromises();
    expect(count("POST", "/review/post")).toBe(1);
    const warn = document.body.querySelector('[data-test="post-conflict"]')!;
    expect(warn.textContent).toContain("fff9999");
    expect(
      document.body
        .querySelector('[data-test="post-confirm"]')
        ?.textContent?.trim(),
    ).toBe("Confirm and post");
    (
      document.body.querySelector('[data-test="post-confirm"]') as HTMLElement
    ).click();
    await flushPromises();
    const second = calls.filter(
      (c) => c.method === "POST" && c.url.endsWith("/review/post"),
    )[1];
    expect(second.body).toMatchObject({
      expect: { head_sha: "fff9999" },
    });
    expect(w.find('[data-test="post-result"]').text()).toContain("boom");
    expect(w.find('[data-test="post-result"]').text()).toContain(
      "Verdict not applied",
    );
    w.unmount();
  });

  it("refreshes on pr_review events for this pull request and stops polling on unmount", async () => {
    const { w } = await mountPR();
    const before = count("GET", "/pull-requests/7/review/drafts");
    const factory = useFactory();
    factory.liveEvents = [
      {
        id: 1,
        work_order_id: "",
        actor: "",
        kind: "pr_review.run_finished",
        message: "",
        payload: { project_id: "p1", number: 7 },
      },
    ];
    await flushPromises();
    expect(count("GET", "/pull-requests/7/review/drafts")).toBeGreaterThan(
      before,
    );
    w.unmount();
  });
});
