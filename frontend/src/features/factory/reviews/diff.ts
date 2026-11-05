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
  note?: string;
}

const HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;
// diff and git append a tab and timestamp to ---/+++ paths.
const headerPath = (line: string) => line.slice(4).split("\t")[0];

// Parses unified diff text (git or plain). Hunk line counts decide where a hunk ends, but a ---/+++/@@ triple always starts a new file, so overstated counts cannot swallow the next one. Rename-only, binary and mode-only git files stay in the list with a note.
export function parseUnifiedDiff(text: string): DiffFile[] {
  const files: DiffFile[] = [],
    rows = text.split(/\r?\n/);
  let file: DiffFile | undefined,
    hunk: DiffHunk | undefined,
    o = 0,
    n = 0,
    oLeft = 0,
    nLeft = 0,
    oldPath = "",
    git = false;
  const start = (path: string, isGit: boolean) => {
    file = { path, add: 0, del: 0, hunks: [] };
    git = isGit;
    files.push(file);
    hunk = undefined;
  };
  const note = (t: string) => {
    if (file) file.note = file.note ? file.note + " · " + t : t;
  };
  for (let i = 0; i < rows.length; i++) {
    const line = rows[i];
    const header =
      line.startsWith("--- ") &&
      rows[i + 1]?.startsWith("+++ ") &&
      rows[i + 2]?.startsWith("@@ ");
    if (hunk && (oLeft > 0 || nLeft > 0) && !header) {
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
      start(line.split(" b/").at(-1) ?? line, true);
      oldPath = "";
    } else if (line.startsWith("--- ")) {
      oldPath = headerPath(line).replace(/^a\//, "");
      if (!file || file.hunks.length || !git) start(oldPath, false);
    } else if (line.startsWith("+++ ") && file) {
      const p = headerPath(line).replace(/^b\//, "");
      file.path = p === "/dev/null" ? oldPath || file.path : p;
    } else if (file && git && line.startsWith("rename from ")) {
      note("renamed from " + line.slice(12));
    } else if (file && git && line.startsWith("rename to ")) {
      file.path = line.slice(10);
    } else if (file && git && /^(Binary files|GIT binary patch)/.test(line)) {
      note("binary file");
    } else if (file && git && /^(old|new) mode /.test(line)) {
      note(line.replace(" mode ", " mode: "));
    } else if (file && git && /^(new|deleted) file mode /.test(line)) {
      note(line.startsWith("new") ? "new file" : "deleted file");
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
  return files.filter((f) => f.hunks.length || f.note);
}

// The backend exposes no diff endpoint, so the only diff text is whatever a builder run printed. Runs apply in order, so hunks accumulate per path; failed runs are skipped.
export function diffFromRuns(runs: Run[]): DiffFile[] {
  const byPath = new Map<string, DiffFile>();
  for (const r of runs) {
    if (
      r.kind !== "builder" ||
      r.state.toLowerCase() === "failed" ||
      !(r.output?.includes("@@ ") || r.output?.includes("diff --git "))
    )
      continue;
    for (const f of parseUnifiedDiff(r.output)) {
      const have = byPath.get(f.path);
      if (!have) byPath.set(f.path, f);
      else {
        have.hunks.push(...f.hunks);
        have.add += f.add;
        have.del += f.del;
        if (f.note && !have.note) have.note = f.note;
      }
    }
  }
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
