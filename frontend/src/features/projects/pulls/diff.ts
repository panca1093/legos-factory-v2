import type { PRFile } from "../../../types/review";

export interface DiffLine {
  t: "add" | "del" | "ctx";
  o?: number;
  n?: number;
  s: string;
}
export interface DiffHunk {
  header: string;
  lines: DiffLine[];
}

const HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

export function parsePatch(patch: string): DiffHunk[] {
  const hunks: DiffHunk[] = [];
  let hunk: DiffHunk | undefined,
    o = 0,
    n = 0,
    oLeft = 0,
    nLeft = 0;
  for (const line of patch.split(/\r?\n/)) {
    const m = HUNK.exec(line);
    if (m) {
      hunk = { header: line, lines: [] };
      hunks.push(hunk);
      o = +m[1];
      n = +m[3];
      oLeft = m[2] === undefined ? 1 : +m[2];
      nLeft = m[4] === undefined ? 1 : +m[4];
      continue;
    }
    if (!hunk || (oLeft <= 0 && nLeft <= 0)) continue;
    const c = line[0];
    if (c === "\\") continue;
    if (c === "+" && nLeft > 0) {
      hunk.lines.push({ t: "add", n: n++, s: line.slice(1) });
      nLeft--;
    } else if (c === "-" && oLeft > 0) {
      hunk.lines.push({ t: "del", o: o++, s: line.slice(1) });
      oLeft--;
    } else if (c === " " || line === "") {
      hunk.lines.push({ t: "ctx", o: o++, n: n++, s: line.slice(1) });
      oLeft--;
      nLeft--;
    }
  }
  return hunks;
}

export type TreeRow = {
  kind: "dir" | "file";
  depth: number;
  name: string;
  path: string;
  file?: PRFile;
};

type Node = { name: string; path: string; dirs: Node[]; files: PRFile[] };

// Single-child folder chains are compacted into one row (internal/adapter).
export function fileTree(files: PRFile[], closed: Set<string>): TreeRow[] {
  const root: Node = { name: "", path: "", dirs: [], files: [] };
  for (const f of files) {
    let cur = root;
    const parts = f.path.split("/");
    parts.slice(0, -1).forEach((part, i) => {
      const path = parts.slice(0, i + 1).join("/");
      let next = cur.dirs.find((d) => d.path === path);
      if (!next) {
        next = { name: part, path, dirs: [], files: [] };
        cur.dirs.push(next);
      }
      cur = next;
    });
    cur.files.push(f);
  }
  const out: TreeRow[] = [];
  const walk = (node: Node, depth: number) => {
    for (let d of [...node.dirs].sort((a, b) => a.name.localeCompare(b.name))) {
      let name = d.name;
      while (d.dirs.length === 1 && !d.files.length) {
        d = d.dirs[0];
        name += "/" + d.name;
      }
      out.push({ kind: "dir", depth, name, path: d.path });
      if (!closed.has(d.path)) walk(d, depth + 1);
    }
    for (const f of node.files)
      out.push({
        kind: "file",
        depth,
        name: f.path.split("/").at(-1) ?? f.path,
        path: f.path,
        file: f,
      });
  };
  walk(root, 0);
  return out;
}

// Scrolls the draft card into view; false when it is not rendered (yet).
export function scrollToDraft(id: string): boolean {
  const el = [
    ...document.querySelectorAll<HTMLElement>('[data-test^="draft-"]'),
  ].find((e) => e.dataset.test === `draft-${id}`);
  el?.scrollIntoView?.({ block: "center", behavior: "smooth" });
  return !!el;
}
