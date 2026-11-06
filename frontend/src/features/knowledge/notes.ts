import type { KBDoc } from "../../types/api";

const esc = (s: string) =>
  s
    .replace(/\uE000/g, "")
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

function emphasis(t: string) {
  return t
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>");
}

function inline(escaped: string) {
  const stash: string[] = [];
  const keep = (html: string) => `\uE000${stash.push(html) - 1}\uE000`;
  return emphasis(
    escaped
      .replace(/`([^`]+)`/g, (_, c) => keep(`<code>${c}</code>`))
      .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, (_, t, u) =>
        keep(
          `<a href="${u}" target="_blank" rel="noopener noreferrer">${emphasis(t)}</a>`,
        ),
      ),
  ).replace(/\uE000(\d+)\uE000/g, (_, i) => stash[+i]);
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
      const level = h[1].length + 2;
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
export const NB_FACTORY = "p:";
export type Notebook = { key: string; label: string; count: number };

export function notebooksOf(
  docs: KBDoc[],
  projects: { id: string; name: string }[],
): Notebook[] {
  const by = new Map<string, Notebook>(
    projects.map((p) => [p.id, { key: `p:${p.id}`, label: p.name, count: 0 }]),
  );
  const factory: Notebook = {
    key: NB_FACTORY,
    label: "Factory-wide",
    count: 0,
  };
  for (const d of docs) {
    const id = d.project_id ?? "";
    if (!id) factory.count++;
    else {
      const nb = by.get(id) ?? { key: `p:${id}`, label: id, count: 0 };
      nb.count++;
      by.set(id, nb);
    }
  }
  const books = [...by.values()].sort((a, b) => a.label.localeCompare(b.label));
  return [
    { key: NB_ALL, label: "All notes", count: docs.length },
    factory,
    ...books,
  ];
}

export function inNotebook(d: KBDoc, key: string): boolean {
  return key === NB_ALL || `p:${d.project_id ?? ""}` === key;
}

export const isProjectNotebook = (key: string) =>
  key.startsWith("p:") && key !== NB_FACTORY;

export const ts = (d: KBDoc) =>
  typeof d.indexed_at === "number" ? d.indexed_at : Date.parse(d.indexed_at);

export type Folder = { key: string; label: string; docs: KBDoc[] };

const PROJECT_FOLDERS = [
  { key: "brief", label: "Brief", types: ["Brief"] },
  { key: "spec", label: "Spec", types: ["Spec"] },
  { key: "task", label: "Task", types: ["Task DAG", "Task Summary"] },
  { key: "notes", label: "Notes", types: ["Note"] },
  { key: "repo", label: "Repository", types: [] as string[] },
  { key: "review", label: "Needs review", types: [] as string[] },
];
const FACTORY_FOLDERS = [
  { key: "playbooks", label: "Playbooks", types: ["Playbook"] },
  { key: "review", label: "Needs review", types: [] as string[] },
];

export function groupFolders(docs: KBDoc[], project: boolean): Folder[] {
  const defs = project ? PROJECT_FOLDERS : FACTORY_FOLDERS;
  const out: Folder[] = [
    ...defs.map((f) => ({ key: f.key, label: f.label, docs: [] as KBDoc[] })),
    { key: "other", label: "Other", docs: [] },
  ];
  const at = (k: string) => out.find((f) => f.key === k)!;
  for (const d of [...docs].sort((a, b) => ts(b) - ts(a))) {
    const byType = defs.find((f) => f.types.includes(d.type))?.key;
    const key =
      d.status === "draft"
        ? "review"
        : (byType ?? (project && d.source === "repo" ? "repo" : "other"));
    at(key).docs.push(d);
  }
  return out.filter((f) => f.key !== "other" || f.docs.length);
}

const ORIGINS: Record<string, string> = {
  work_order: "Work order",
  chat: "Chat",
  handoff: "Handoff",
  manual: "Manual",
};
export const originLabel = (d: KBDoc) => ORIGINS[d.meta?.origin ?? ""] ?? "";
