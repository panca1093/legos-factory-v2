import type { Chat, ChatStep } from "../../stores/chats";

export const MAX_MESSAGE_BYTES = 32 * 1024;
export const MAX_FILES = 5;
export const MAX_FILE_BYTES = 100 * 1024;
export const MAX_TOTAL_BYTES = 300 * 1024;

export const MAX_IMAGE_B64 = 5_242_880;
export const MAX_IMAGES_B64_TOTAL = 10_485_760;
const IMAGE_EXT = /\.(png|jpe?g|gif|webp)$/i;

export type ImageType = "image/png" | "image/jpeg" | "image/gif" | "image/webp";

export interface PendingFile {
  name: string;
  size: number;
  content: string;
  encoding?: "base64";
  kind?: "file" | "image";
  mediaType?: ImageType;
  preview?: string;
}

export const base64Size = (b64: string) =>
  Math.floor((b64.length * 3) / 4) -
  (b64.endsWith("==") ? 2 : b64.endsWith("=") ? 1 : 0);

const startsWith = (b: Uint8Array, sig: number[], at = 0) =>
  sig.every((v, i) => b[at + i] === v);

export function detectImage(b: Uint8Array): ImageType | undefined {
  if (startsWith(b, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))
    return "image/png";
  if (startsWith(b, [0xff, 0xd8, 0xff])) return "image/jpeg";
  if (
    startsWith(b, [0x47, 0x49, 0x46, 0x38, 0x37, 0x61]) ||
    startsWith(b, [0x47, 0x49, 0x46, 0x38, 0x39, 0x61])
  )
    return "image/gif";
  if (
    startsWith(b, [0x52, 0x49, 0x46, 0x46]) &&
    startsWith(b, [0x57, 0x45, 0x42, 0x50], 8)
  )
    return "image/webp";
}

function toBase64(buf: ArrayBuffer) {
  const bytes = new Uint8Array(buf);
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000)
    bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

export const byteLength = (s: string) => new TextEncoder().encode(s).length;
export const formatSize = (n: number) =>
  n < 1024 ? `${n} B` : `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
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

function readBuffer(file: Blob): Promise<ArrayBuffer> {
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
  let total = files.reduce((n, f) => n + (f.encoding ? 0 : f.size), 0);
  let imageTotal = files.reduce(
    (n, f) => n + (f.encoding ? f.content.length : 0),
    0,
  );
  for (const file of incoming) {
    if (files.length >= MAX_FILES) {
      errors.push(`Up to ${MAX_FILES} files per message.`);
      break;
    }
    const mediaType = detectImage(
      new Uint8Array(await readBuffer(file.slice(0, 12))),
    );
    if (mediaType || IMAGE_EXT.test(file.name)) {
      if (!mediaType) {
        errors.push(
          `${file.name} is not a supported image (PNG, JPEG, GIF or WebP).`,
        );
        continue;
      }
      const length = 4 * Math.ceil(file.size / 3);
      if (length > MAX_IMAGE_B64) {
        errors.push(`${file.name} is over 3.75 MB.`);
        continue;
      }
      if (imageTotal + length > MAX_IMAGES_B64_TOTAL) {
        errors.push("Images can total 7.5 MB at most.");
        continue;
      }
      const content = toBase64(await readBuffer(file));
      files.push({
        name: file.name,
        size: file.size,
        content,
        encoding: "base64",
        kind: "image",
        mediaType,
        preview: `data:${mediaType};base64,${content}`,
      });
      imageTotal += length;
      continue;
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
