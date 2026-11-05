import type { Chat } from "../../stores/chats";

export const MAX_MESSAGE_BYTES = 32 * 1024;
export const MAX_FILES = 5;
export const MAX_FILE_BYTES = 100 * 1024;
export const MAX_TOTAL_BYTES = 300 * 1024;

export interface PendingFile {
  name: string;
  size: number;
  content: string;
}

export const byteLength = (s: string) => new TextEncoder().encode(s).length;
export const formatSize = (n: number) =>
  n < 1024 ? `${n} B` : `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
export const chatTitle = (c?: Pick<Chat, "title">) => c?.title || "New chat";

export function groupByRecency(chats: Chat[], now = new Date()) {
  const day = (d: Date) =>
    new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const today = day(now);
  const DAY = 86_400_000;
  const labels = ["Today", "Yesterday", "Previous 7 days", "Older"];
  const groups = labels.map((label) => ({ label, chats: [] as Chat[] }));
  for (const c of chats) {
    const age = Math.round((today - day(new Date(c.updated_at))) / DAY);
    groups[age <= 0 ? 0 : age === 1 ? 1 : age <= 7 ? 2 : 3].chats.push(c);
  }
  return groups.filter((g) => g.chats.length);
}

function readBuffer(file: File): Promise<ArrayBuffer> {
  if (typeof file.arrayBuffer === "function") return file.arrayBuffer();
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result as ArrayBuffer);
    r.onerror = () => reject(r.error);
    r.readAsArrayBuffer(file);
  });
}

export async function addFiles(
  current: PendingFile[],
  incoming: File[],
): Promise<{ files: PendingFile[]; errors: string[] }> {
  const files = [...current];
  const errors: string[] = [];
  let total = files.reduce((n, f) => n + f.size, 0);
  for (const file of incoming) {
    if (files.length >= MAX_FILES) {
      errors.push(`Up to ${MAX_FILES} files per message.`);
      break;
    }
    if (file.size > MAX_FILE_BYTES) {
      errors.push(`${file.name} is over ${MAX_FILE_BYTES / 1024} KB.`);
      continue;
    }
    if (total + file.size > MAX_TOTAL_BYTES) {
      errors.push(
        `Attachments can total ${MAX_TOTAL_BYTES / 1024} KB at most.`,
      );
      continue;
    }
    let content: string;
    try {
      content = new TextDecoder("utf-8", { fatal: true }).decode(
        await readBuffer(file),
      );
    } catch {
      errors.push(`${file.name} is not a UTF-8 text file.`);
      continue;
    }
    if (content.includes("\0")) {
      errors.push(`${file.name} is not a UTF-8 text file.`);
      continue;
    }
    files.push({ name: file.name, size: file.size, content });
    total += file.size;
  }
  return { files, errors: [...new Set(errors)] };
}

const esc = (s: string) =>
  s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");

function inline(escaped: string) {
  return escaped
    .split("`")
    .map((part, i) => {
      if (i % 2) return `<code>${part}</code>`;
      return part
        .replace(
          /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
          '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>',
        )
        .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
        .replace(
          /(^|[\s(])\*([^*\s][^*]*)\*(?=$|[\s).,;:!?])/g,
          "$1<em>$2</em>",
        );
    })
    .join("");
}

function blocks(text: string) {
  const out: string[] = [];
  let list: { tag: "ul" | "ol"; items: string[] } | null = null;
  let para: string[] = [];
  const flush = () => {
    if (para.length) out.push(`<p>${para.join("<br>")}</p>`);
    para = [];
    if (list)
      out.push(
        `<${list.tag}>${list.items.map((i) => `<li>${i}</li>`).join("")}</${list.tag}>`,
      );
    list = null;
  };
  for (const line of esc(text).split("\n")) {
    const item = /^\s*(?:([-*])|\d+\.)\s+(.*)$/.exec(line);
    const head = /^#{1,6}\s+(.*)$/.exec(line);
    if (item) {
      const tag = item[1] ? "ul" : "ol";
      if (para.length || (list && list.tag !== tag)) flush();
      (list ??= { tag, items: [] }).items.push(inline(item[2]));
    } else if (head) {
      flush();
      out.push(`<h4>${inline(head[1])}</h4>`);
    } else if (!line.trim()) flush();
    else {
      if (list) flush();
      para.push(inline(line));
    }
  }
  flush();
  return out.join("");
}

// Output is built only from escaped text plus a fixed tag set, so it is safe for v-html.
export function renderMarkdown(src: string) {
  const out: string[] = [];
  const re = /```([^\n`]*)\n([\s\S]*?)(?:```|$)/g;
  let last = 0;
  for (let m = re.exec(src); m; m = re.exec(src)) {
    out.push(blocks(src.slice(last, m.index)));
    const lang = m[1].trim();
    out.push(
      `<pre${lang ? ` data-lang="${esc(lang)}"` : ""}><code>${esc(m[2].replace(/\n$/, ""))}</code></pre>`,
    );
    last = m.index + m[0].length;
    if (m[0].length === 0) re.lastIndex++;
  }
  out.push(blocks(src.slice(last)));
  return out.join("");
}
