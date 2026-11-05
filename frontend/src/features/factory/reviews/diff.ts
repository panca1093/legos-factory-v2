import type { Run } from "../../../types/api";

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
export interface DiffFile {
  path: string;
  add: number;
  del: number;
  hunks: DiffHunk[];
}

const HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

// Parses unified diff text (git or plain). Hunk line counts decide where a hunk ends, so a deleted "-- x" line is never read as a file header.
export function parseUnifiedDiff(text: string): DiffFile[] {
  const files: DiffFile[] = [];
  let file: DiffFile | undefined,
    hunk: DiffHunk | undefined,
    o = 0,
    n = 0,
    oLeft = 0,
    nLeft = 0,
    oldPath = "";
  const start = (path: string) => {
    file = { path, add: 0, del: 0, hunks: [] };
    files.push(file);
    hunk = undefined;
  };
  for (const line of text.split("\n")) {
    if (hunk && (oLeft > 0 || nLeft > 0)) {
      const c = line[0];
      if (c === "\\") continue;
      if (c === "+" && nLeft > 0) {
        hunk.lines.push({ t: "add", n: n++, s: line.slice(1) });
        nLeft--;
        file!.add++;
        continue;
      }
      if (c === "-" && oLeft > 0) {
        hunk.lines.push({ t: "del", o: o++, s: line.slice(1) });
        oLeft--;
        file!.del++;
        continue;
      }
      if (c === " " || line === "") {
        hunk.lines.push({ t: "ctx", o: o++, n: n++, s: line.slice(1) });
        oLeft--;
        nLeft--;
        continue;
      }
    }
    hunk = undefined;
    if (line.startsWith("diff --git ")) {
      start(line.split(" b/").at(-1) ?? line);
      oldPath = "";
    } else if (line.startsWith("--- ")) {
      oldPath = line.slice(4).replace(/^a\//, "");
      if (!file || file.hunks.length) start(oldPath);
    } else if (line.startsWith("+++ ") && file) {
      const p = line.slice(4).replace(/^b\//, "");
      file.path = p === "/dev/null" ? oldPath || file.path : p;
    } else {
      const m = HUNK.exec(line);
      if (m && file) {
        o = +m[1];
        n = +m[3];
        oLeft = m[2] === undefined ? 1 : +m[2];
        nLeft = m[4] === undefined ? 1 : +m[4];
        hunk = { header: line, lines: [] };
        file.hunks.push(hunk);
      }
    }
  }
  return files.filter((f) => f.hunks.length);
}

// The backend exposes no diff endpoint, so the only diff text is whatever a builder run printed.
export function diffFromRuns(runs: Run[]): DiffFile[] {
  const byPath = new Map<string, DiffFile>();
  for (const r of runs)
    if (r.kind === "builder" && r.output?.includes("@@ "))
      for (const f of parseUnifiedDiff(r.output)) byPath.set(f.path, f);
  return [...byPath.values()];
}

// Pairs each run of deletions with the additions that follow it, side by side.
export function splitRows(h: DiffHunk): [DiffLine | null, DiffLine | null][] {
  const rows: [DiffLine | null, DiffLine | null][] = [];
  let dels: DiffLine[] = [],
    adds: DiffLine[] = [];
  const flush = () => {
    for (let i = 0; i < Math.max(dels.length, adds.length); i++)
      rows.push([dels[i] ?? null, adds[i] ?? null]);
    dels = [];
    adds = [];
  };
  for (const l of h.lines) {
    if (l.t === "del") dels.push(l);
    else if (l.t === "add") adds.push(l);
    else {
      flush();
      rows.push([l, l]);
    }
  }
  flush();
  return rows;
}
