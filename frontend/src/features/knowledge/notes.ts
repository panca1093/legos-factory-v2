import type { KBDoc } from "../../types/api";

const esc = (s: string) =>
  s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
const slug = (s: string) =>
  "h-" +
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");

function inline(escaped: string) {
  return escaped
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>")
    .replace(
      /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
      '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>',
    );
}

export type TocItem = { id: string; text: string };

export function tocOf(body: string): TocItem[] {
  const out: TocItem[] = [];
  const seen = new Map<string, number>();
  let code = false;
  for (const line of body.split("\n")) {
    if (/^\s*```/.test(line)) code = !code;
    const h = !code && /^#{1,3}\s+(.*?)\s*#*$/.exec(line);
    if (!h) continue;
    const base = slug(h[1]);
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    out.push({ id: n ? `${base}-${n}` : base, text: h[1] });
  }
  return out;
}

export function renderMarkdown(src: string): string {
  const out: string[] = [];
  const toc = tocOf(src);
  let ti = 0;
  let list: "ul" | "ol" | null = null;
  let code: string[] | null = null;
  const closeList = () => {
    if (list) out.push(`</${list}>`);
    list = null;
  };
  for (const raw of src.split("\n")) {
    if (/^\s*```/.test(raw)) {
      if (code) {
        out.push(`<pre><code>${code.join("\n")}</code></pre>`);
        code = null;
      } else {
        closeList();
        code = [];
      }
      continue;
    }
    const line = esc(raw);
    if (code) {
      code.push(line);
      continue;
    }
    const h = /^(#{1,3})\s+(.*?)\s*#*$/.exec(line);
    const li = /^\s*([-*]|\d+\.)\s+(.*)$/.exec(line);
    const q = /^&gt;\s?(.*)$/.exec(line);
    if (h) {
      closeList();
      const level = h[1].length + 1;
      out.push(
        `<h${level} id="${toc[ti++]?.id ?? slug(h[2])}">${inline(h[2])}</h${level}>`,
      );
    } else if (li) {
      const t = /\d/.test(li[1]) ? "ol" : "ul";
      if (list !== t) {
        closeList();
        out.push(`<${t}>`);
        list = t;
      }
      out.push(`<li>${inline(li[2])}</li>`);
    } else if (q) {
      closeList();
      out.push(`<blockquote>${inline(q[1])}</blockquote>`);
    } else {
      closeList();
      if (line.trim()) out.push(`<p>${inline(line)}</p>`);
    }
  }
  if (code) out.push(`<pre><code>${code.join("\n")}</code></pre>`);
  closeList();
  return out.join("");
}

const reEsc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

export function highlight(text: string, q: string): string {
  const terms = q
    .split(/\s+/)
    .filter(Boolean)
    .map((t) => reEsc(esc(t)));
  const safe = esc(text);
  if (!terms.length) return safe;
  return safe.replace(
    new RegExp(`(&[a-z]+;)|(${terms.join("|")})`, "gi"),
    (m, entity) => (entity ? m : `<mark>${m}</mark>`),
  );
}

export function snippet(text: string, q: string, max = 120): string {
  const plain = text
    .replace(/[#*`>]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  const first = q.split(/\s+/).filter(Boolean)[0];
  const i = first ? plain.toLowerCase().indexOf(first.toLowerCase()) : -1;
  if (i > 30) return "…" + plain.slice(i - 30, i - 30 + max);
  return plain.slice(0, max);
}

export function when(t: number | string, now = Date.now()): string {
  const ms = typeof t === "number" ? (t < 1e12 ? t * 1000 : t) : Date.parse(t);
  if (!Number.isFinite(ms)) return "";
  const d = (now - ms) / 86_400_000;
  if (d < 1) return "Today";
  if (d < 2) return "Yesterday";
  if (d < 7) return `${Math.floor(d)} days ago`;
  return new Date(ms).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
  });
}

export const NB_ALL = "all";
export const NB_DRAFTS = "drafts";
export type Notebook = { key: string; label: string; count: number };

export function notebooksOf(
  docs: KBDoc[],
  projectName: (id?: string) => string,
): Notebook[] {
  const live = docs.filter((d) => d.status !== "draft");
  const drafts = docs.length - live.length;
  const by = new Map<string, Notebook>();
  for (const d of live) {
    const key = d.project_id ?? "";
    const nb = by.get(key) ?? {
      key: `p:${key}`,
      label: projectName(d.project_id),
      count: 0,
    };
    nb.count++;
    by.set(key, nb);
  }
  const books = [...by.values()].sort((a, b) => a.label.localeCompare(b.label));
  const out: Notebook[] = [
    { key: NB_ALL, label: "All notes", count: live.length },
  ];
  if (drafts)
    out.push({ key: NB_DRAFTS, label: "Drafts to review", count: drafts });
  return [...out, ...books];
}

export function inNotebook(d: KBDoc, key: string): boolean {
  if (key === NB_ALL) return d.status !== "draft";
  if (key === NB_DRAFTS) return d.status === "draft";
  return d.status !== "draft" && `p:${d.project_id ?? ""}` === key;
}
