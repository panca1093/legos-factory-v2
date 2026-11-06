import { describe, expect, it } from "vitest";
import {
  groupFolders,
  highlight,
  inNotebook,
  notebooksOf,
  renderMarkdown,
  snippet,
  tocOf,
} from "../../src/features/knowledge/notes";
import type { KBDoc } from "../../src/types/api";

it("highlights search terms after escaping and never inside entities", () => {
  expect(highlight("Rounding <b> rules", "round")).toBe(
    "<mark>Round</mark>ing &lt;b&gt; rules",
  );
  expect(highlight("a < b", "lt")).toBe("a &lt; b");
  expect(highlight("plain", "")).toBe("plain");
});

it("snippets centre on the first match", () => {
  const text = "x".repeat(80) + " needle " + "y".repeat(200);
  const s = snippet(text, "needle");
  expect(s.startsWith("…")).toBe(true);
  expect(s).toContain("needle");
  expect(snippet("## Short *note*", "")).toBe("Short note");
});

it("builds a table of contents from headings outside code fences", () => {
  const body =
    "# One\n\ntext\n\n## Two\n```\n## not a heading\n```\n## Two\n#### skip";
  expect(tocOf(body)).toEqual([
    { id: "h-one", text: "One" },
    { id: "h-two", text: "Two" },
    { id: "h-two-1", text: "Two" },
  ]);
  const html = renderMarkdown(body);
  expect(html).toContain('<h3 id="h-one">One</h3>');
  expect(html).toContain('<h4 id="h-two-1">Two</h4>');
});

it("renders markdown without letting raw html or script urls through", () => {
  const html = renderMarkdown(
    '<img src=x onerror=alert(1)>\n\n[bad](javascript:alert(1)) [ok](https://a.dev/x?a=1&b="2")\n\n- **bold** `<i>`\n\n```\n<script>\n```',
  );
  expect(html).not.toContain("<img");
  expect(html).not.toContain("<script");
  expect(html).not.toContain('href="javascript');
  expect(html).toContain("&lt;img");
  expect(html).toContain('<a href="https://a.dev/x?a=1&amp;b=&quot;2&quot;"');
  expect(html).toContain(
    "<li><strong>bold</strong> <code>&lt;i&gt;</code></li>",
  );
});

const doc = (over: Partial<KBDoc>): KBDoc => ({
  id: "i",
  source: "factory",
  path: "p.md",
  commit_sha: "",
  content_hash: "",
  type: "",
  status: "stable",
  trust: "human",
  title: "t",
  description: "",
  meta: {},
  indexed_at: 1,
  ...over,
});

it("lists All, Factory-wide and every project, even without documents", () => {
  const docs = [
    doc({ id: "a" }),
    doc({ id: "b", project_id: "p1" }),
    doc({ id: "c", project_id: "p1", status: "draft" }),
  ];
  const nbs = notebooksOf(docs, [
    { id: "p1", name: "Greeter" },
    { id: "p2", name: "Empty" },
  ]);
  expect(nbs.map((n) => [n.key, n.count])).toEqual([
    ["all", 3],
    ["p:", 1],
    ["p:p2", 0],
    ["p:p1", 2],
  ]);
  expect(docs.filter((d) => inNotebook(d, "p:")).map((d) => d.id)).toEqual([
    "a",
  ]);
  expect(docs.filter((d) => inNotebook(d, "p:p1"))).toHaveLength(2);
});

describe("groupFolders", () => {
  const ids = (fs: ReturnType<typeof groupFolders>) =>
    fs.map((f) => [f.key, f.docs.map((d) => d.id)]);
  it("places each project doc in exactly one folder, newest first", () => {
    const docs = [
      doc({ id: "b1", type: "Brief", indexed_at: 1 }),
      doc({ id: "b2", type: "Brief", indexed_at: 2 }),
      doc({ id: "s", type: "Spec" }),
      doc({ id: "t1", type: "Task DAG" }),
      doc({ id: "t2", type: "Task Summary" }),
      doc({ id: "n", type: "Note" }),
      doc({ id: "r", type: "Guide", source: "repo" }),
      doc({ id: "rs", type: "Spec", source: "repo" }),
      doc({ id: "d", type: "Note", status: "draft" }),
      doc({ id: "dep", type: "Note", status: "deprecated", indexed_at: 0 }),
    ];
    const out = groupFolders(docs, true);
    expect(ids(out)).toEqual([
      ["brief", ["b2", "b1"]],
      ["spec", ["s", "rs"]],
      ["task", ["t1", "t2"]],
      ["notes", ["n", "dep"]],
      ["repo", ["r"]],
      ["review", ["d"]],
    ]);
    expect(out.flatMap((f) => f.docs)).toHaveLength(docs.length);
  });
  it("keeps every project folder when empty and adds Other only when needed", () => {
    expect(groupFolders([], true).map((f) => f.label)).toEqual([
      "Brief",
      "Spec",
      "Task",
      "Notes",
      "Repository",
      "Needs review",
    ]);
    const out = groupFolders([doc({ id: "x", type: "Weird" })], true);
    expect(out.at(-1)).toMatchObject({ label: "Other" });
    expect(out.at(-1)!.docs.map((d) => d.id)).toEqual(["x"]);
  });
  it("groups factory-wide docs into Playbooks, Needs review and Other", () => {
    const out = groupFolders(
      [
        doc({ id: "p", type: "Playbook" }),
        doc({ id: "d", type: "Reviewer Routing", status: "draft" }),
        doc({ id: "o", type: "Note" }),
      ],
      false,
    );
    expect(ids(out)).toEqual([
      ["playbooks", ["p"]],
      ["review", ["d"]],
      ["other", ["o"]],
    ]);
  });
});

it("keeps emphasis markers inside urls and code out of the formatting pass", () => {
  const html = renderMarkdown(
    "[a *b*](https://x.dev/*y*/_z_) and `*not em*` and *em*",
  );
  expect(html).toContain('href="https://x.dev/*y*/_z_"');
  expect(html).toContain(">a <em>b</em></a>");
  expect(html).toContain("<code>*not em*</code>");
  expect(html).toContain("<em>em</em>");
});
