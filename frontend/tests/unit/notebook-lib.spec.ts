import { expect, it } from "vitest";
import {
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

it("groups notes into notebooks and keeps drafts separate", () => {
  const docs = [
    doc({ id: "a" }),
    doc({ id: "b", project_id: "p1" }),
    doc({ id: "c", project_id: "p1" }),
    doc({ id: "d", status: "draft" }),
  ];
  const nbs = notebooksOf(
    docs.filter((d) => d.status !== "draft"),
    (id) => (id ? "Greeter" : "Factory-wide"),
    1,
  );
  expect(nbs.map((n) => [n.key, n.count])).toEqual([
    ["all", 3],
    ["drafts", 1],
    ["p:", 1],
    ["p:p1", 2],
  ]);
  expect(docs.filter((d) => inNotebook(d, "p:")).map((d) => d.id)).toEqual([
    "a",
    "d",
  ]);
  expect(docs.filter((d) => inNotebook(d, "p:p1"))).toHaveLength(2);
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
