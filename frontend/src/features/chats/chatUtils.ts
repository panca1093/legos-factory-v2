import type { Chat, ChatStep } from "../../stores/chats";

export const MAX_MESSAGE_BYTES = 32 * 1024;
export const MAX_FILES = 10;
const MB = 1024 * 1024;
export const MAX_TOTAL_BYTES = 25 * MB;
export const KIND_LIMITS = { image: 5 * MB, pdf: 20 * MB, text: MB } as const;
export type FileKind = keyof typeof KIND_LIMITS;

export type UploadStatus = "uploading" | "done" | "error";
export interface ChatUpload {
  key: string;
  name: string;
  size: number;
  kind: FileKind;
  status: UploadStatus;
  progress: number;
  error?: string;
  id?: string;
  preview?: string;
  file: File;
}

export const MSG_OFFICE =
  "Word, Excel and PowerPoint files are not supported yet. Export as PDF or text.";
export const MSG_OTHER =
  "Only images (PNG, JPEG, GIF, WebP), PDF and text files can be attached.";

const IMAGE_EXT = /\.(png|jpe?g|gif|webp)$/i;
const IMAGE_MIME = /^image\/(png|jpeg|gif|webp)$/;
const OFFICE_EXT = /\.(docx?|xlsx?|pptx?)$/i;
const OFFICE_MIME =
  /^application\/(msword|vnd\.ms-(excel|powerpoint)|vnd\.openxmlformats-officedocument\..+)$/;
const BINARY_EXT =
  /\.(zip|gz|tgz|tar|7z|rar|exe|dmg|bin|iso|mp3|wav|mp4|mov|avi|mkv|heic|heif|tiff?|bmp|psd|ico|woff2?|ttf|otf)$/i;
const BINARY_MIME =
  /^(audio|video|font)\/|^image\/(?!svg)|^application\/(zip|gzip|x-tar|x-7z-compressed|vnd\.rar|x-msdownload)$/;

// By name and MIME only; the server decides from the bytes, so unknown names stay text candidates.
export function classifyFile(f: {
  name: string;
  type: string;
}): FileKind | "office" | "other" {
  if (IMAGE_EXT.test(f.name) || IMAGE_MIME.test(f.type)) return "image";
  if (/\.pdf$/i.test(f.name) || f.type === "application/pdf") return "pdf";
  if (OFFICE_EXT.test(f.name) || OFFICE_MIME.test(f.type)) return "office";
  if (BINARY_EXT.test(f.name) || BINARY_MIME.test(f.type)) return "other";
  return "text";
}

export function precheckFile(
  f: { name: string; type: string; size: number },
  existing: { size: number }[],
): { kind: FileKind } | { error: string } {
  const kind = classifyFile(f);
  if (kind === "office") return { error: MSG_OFFICE };
  if (kind === "other") return { error: MSG_OTHER };
  if (existing.length >= MAX_FILES)
    return { error: `Up to ${MAX_FILES} files per message.` };
  if (f.size === 0) return { error: `${f.name} is empty.` };
  if (f.size > KIND_LIMITS[kind])
    return { error: `${f.name} is over ${formatSize(KIND_LIMITS[kind])}.` };
  if (existing.reduce((n, e) => n + e.size, 0) + f.size > MAX_TOTAL_BYTES)
    return {
      error: `Attachments can total ${formatSize(MAX_TOTAL_BYTES)} at most.`,
    };
  return { kind };
}

export const fileSig = (f: {
  name: string;
  size: number;
  lastModified?: number;
}) => `${f.name}:${f.size}:${f.lastModified ?? 0}`;

export const chatFileUrl = (chatId: string, fileId: string) =>
  `/api/v1/chats/${encodeURIComponent(chatId)}/files/${encodeURIComponent(fileId)}`;

export const byteLength = (s: string) => new TextEncoder().encode(s).length;
export const formatSize = (n: number) =>
  n < 1024
    ? `${n} B`
    : n < MB
      ? `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`
      : `${(n / MB).toFixed(n < 10 * MB ? 1 : 0)} MB`;
export const chatTitle = (c?: Pick<Chat, "title">) => c?.title || "New chat";

// agents name tools differently ("Read File", "read", "grep", "Glob"), so the verb comes from the name's first word
export function stepLine(s: ChatStep) {
  const first = s.name.trim().split(/\s+/)[0].toLowerCase();
  const kind = ["read", "view"].includes(first)
    ? "read"
    : ["grep", "glob", "search", "find", "list"].includes(first)
      ? "search"
      : "";
  const verb = !kind
    ? s.name
    : s.failed
      ? `Could not ${kind}`
      : kind === "read"
        ? "Reading"
        : "Searching";
  return s.target ? `${verb} ${s.target}` : verb;
}

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

const esc = (s: string) =>
  s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");

const LINK = /\[([^\]]+)\]\((https?:\/\/(?:[^\s()]|\([^\s()]*\))+)\)/g;

function emphasis(t: string) {
  return t
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[\s(])\*([^*\s][^*]*)\*(?=$|[\s).,;:!?])/g, "$1<em>$2</em>");
}

function inline(escaped: string) {
  const links: string[] = [];
  return escaped
    .replace(/[\uE000\uE001]/g, "")
    .split("`")
    .map((part, i) => {
      if (i % 2) return `<code>${part}</code>`;
      const held = part.replace(LINK, (_, text: string, url: string) => {
        links.push(
          `<a href="${url}" target="_blank" rel="noopener noreferrer">${emphasis(text)}</a>`,
        );
        return `\uE000${links.length - 1}\uE001`;
      });
      return emphasis(held).replace(
        /\uE000(\d+)\uE001/g,
        (_, n: string) => links[Number(n)],
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

const mdCache = new Map<string, { src: string; html: string }>();
export function renderMarkdownCached(key: string, src: string) {
  const hit = mdCache.get(key);
  if (hit?.src === src) return hit.html;
  const html = renderMarkdown(src);
  mdCache.set(key, { src, html });
  return html;
}
