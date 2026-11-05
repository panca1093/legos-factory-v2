import { expect, it, vi, afterEach } from "vitest";
import {
  highlight,
  inNotebook,
  notebooksOf,
  renderMarkdown,
  snippet,
  tocOf,
} from "../../src/features/knowledge/notes";
import { parseFrames, streamChat } from "../../src/services/api/chatStream";
import type { KBDoc } from "../../src/types/api";

afterEach(() => vi.unstubAllGlobals());

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
  expect(html).toContain('<h2 id="h-one">One</h2>');
  expect(html).toContain('<h3 id="h-two-1">Two</h3>');
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
  const nbs = notebooksOf(docs, (id) => (id ? "Greeter" : "Factory-wide"));
  expect(nbs.map((n) => [n.key, n.count])).toEqual([
    ["all", 3],
    ["drafts", 1],
    ["p:", 1],
    ["p:p1", 2],
  ]);
  expect(docs.filter((d) => inNotebook(d, "drafts")).map((d) => d.id)).toEqual([
    "d",
  ]);
  expect(docs.filter((d) => inNotebook(d, "p:p1"))).toHaveLength(2);
});

it("parses SSE frames across chunk boundaries", () => {
  const a = parseFrames('event: delta\ndata: {"text":"Hel"}\n\nevent: del');
  expect(a.events).toEqual([{ event: "delta", data: { text: "Hel" } }]);
  const b = parseFrames(a.rest + 'ta\r\ndata: {"text":"lo"}\r\n\r\n');
  expect(b.events).toEqual([{ event: "delta", data: { text: "lo" } }]);
  expect(parseFrames("event: x\ndata: {nope}\n\n").events).toEqual([]);
});

function sse(chunks: string[]) {
  const enc = new TextEncoder();
  return new Response(
    new ReadableStream({
      start(c) {
        for (const x of chunks) c.enqueue(enc.encode(x));
        c.close();
      },
    }),
    { status: 200, headers: { "Content-Type": "text/event-stream" } },
  );
}

it("streams events in order and requires a terminal frame", async () => {
  const msg = {
    id: "m",
    chat_id: "c",
    role: "assistant",
    content: "Hi",
    created_at: "",
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      sse([
        'event: delta\ndata: {"text":"H"}\n\nevent: do',
        `ne\ndata: ${JSON.stringify({ message: msg })}\n\n`,
      ]),
    ),
  );
  const seen: string[] = [];
  await streamChat("c1", { content: "q" }, (e) => seen.push(e.event));
  expect(seen).toEqual(["delta", "done"]);

  vi.stubGlobal(
    "fetch",
    vi.fn(async () => sse(['event: delta\ndata: {"text":"H"}\n\n'])),
  );
  await expect(streamChat("c1", { content: "q" }, () => {})).rejects.toThrow(
    "ended unexpectedly",
  );
});

it("turns pre-stream JSON errors into ApiError", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({ error: { code: "CHAT_BUSY", message: "busy" } }),
          {
            status: 409,
          },
        ),
    ),
  );
  await expect(
    streamChat("c1", { content: "q" }, () => {}),
  ).rejects.toMatchObject({
    status: 409,
    code: "CHAT_BUSY",
    message: "busy",
  });
});
