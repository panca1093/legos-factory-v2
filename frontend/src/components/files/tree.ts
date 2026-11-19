export interface TreeEntry {
  name: string;
  kind: "file" | "dir" | "symlink";
  ignored?: boolean;
}

export interface TreeRow {
  path: string;
  tail: string;
  label: string;
  depth: number;
  kind: "file" | "dir" | "symlink";
  open: boolean;
  ignored: boolean;
}

export const ROW_HEIGHT = 24;

export const joinPath = (dir: string, name: string) =>
  dir ? `${dir}/${name}` : name;

export function sortEntries<T extends TreeEntry>(entries: T[]): T[] {
  return [...entries].sort((a, b) => {
    const da = a.kind === "dir" ? 0 : 1,
      db = b.kind === "dir" ? 0 : 1;
    return (
      da - db ||
      a.name.toLowerCase().localeCompare(b.name.toLowerCase()) ||
      a.name.localeCompare(b.name)
    );
  });
}

// A folder whose only child is a loaded folder is shown as one row ("a/b/c"); `path` stays the head so expansion is keyed by it.
export function flattenTree(
  list: (dir: string) => TreeEntry[] | undefined,
  open: ReadonlySet<string>,
  root = "",
): TreeRow[] {
  const out: TreeRow[] = [];
  const walk = (dir: string, depth: number) => {
    const entries = list(dir);
    if (!entries) return;
    for (const e of sortEntries(entries)) {
      const path = joinPath(dir, e.name);
      if (e.kind !== "dir") {
        out.push({
          path,
          tail: path,
          label: e.name,
          depth,
          kind: e.kind,
          open: false,
          ignored: !!e.ignored,
        });
        continue;
      }
      let tail = path,
        label = e.name;
      for (;;) {
        const kids = list(tail);
        if (kids?.length !== 1 || kids[0].kind !== "dir") break;
        tail = joinPath(tail, kids[0].name);
        label += `/${kids[0].name}`;
      }
      const isOpen = open.has(path);
      out.push({
        path,
        tail,
        label,
        depth,
        kind: "dir",
        open: isOpen,
        ignored: !!e.ignored,
      });
      if (isOpen) walk(tail, depth + 1);
    }
  };
  walk(root, 0);
  return out;
}

export function visibleWindow(
  total: number,
  scrollTop: number,
  viewport: number,
  rowHeight = ROW_HEIGHT,
  overscan = 6,
): { start: number; end: number } {
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(
    total,
    Math.ceil((scrollTop + viewport) / rowHeight) + overscan,
  );
  return { start, end: Math.max(start, end) };
}

export type Marker = "M" | "U" | "A" | "D" | "!";
const strength: Record<Marker, number> = { "!": 5, D: 4, M: 3, A: 2, U: 1 };

export function markerOf(x: string, y: string): Marker | null {
  if (x === "?" || y === "?") return "U";
  if (
    x === "U" ||
    y === "U" ||
    (x === "A" && y === "A") ||
    (x === "D" && y === "D")
  )
    return "!";
  if (x === "D" || y === "D") return "D";
  if (x === "A") return "A";
  if (
    x === "M" ||
    y === "M" ||
    x === "R" ||
    y === "R" ||
    x === "T" ||
    y === "T"
  )
    return "M";
  return null;
}

export interface Markers {
  files: Map<string, Marker>;
  dirs: Map<string, Marker>;
  untrackedDirs: string[];
  changed: number;
}

export function rollupMarkers(
  entries: { path: string; x: string; y: string }[],
): Markers {
  const files = new Map<string, Marker>(),
    dirs = new Map<string, Marker>(),
    untrackedDirs: string[] = [];
  const bump = (path: string, m: Marker) => {
    let i = path.lastIndexOf("/");
    while (i > 0) {
      const d = path.slice(0, i);
      const cur = dirs.get(d);
      if (!cur || strength[m] > strength[cur]) dirs.set(d, m);
      i = d.lastIndexOf("/");
    }
  };
  for (const e of entries) {
    const m = markerOf(e.x, e.y);
    if (!m) continue;
    if (e.path.endsWith("/")) {
      const dir = e.path.slice(0, -1);
      untrackedDirs.push(dir);
      dirs.set(dir, m);
      bump(dir, m);
      continue;
    }
    files.set(e.path, m);
    bump(e.path, m);
  }
  return { files, dirs, untrackedDirs, changed: entries.length };
}

export function fileMarker(m: Markers, path: string): Marker | undefined {
  const direct = m.files.get(path);
  if (direct) return direct;
  return m.untrackedDirs.some((d) => path.startsWith(`${d}/`))
    ? "U"
    : undefined;
}

export function dirMarker(m: Markers, path: string): Marker | undefined {
  return (
    m.dirs.get(path) ??
    (m.untrackedDirs.some((d) => path.startsWith(`${d}/`)) ? "U" : undefined)
  );
}

// Per-file error counts, rolled up to every ancestor folder.
export function rollupCounts(
  perFile: Map<string, number>,
): Map<string, number> {
  const out = new Map<string, number>();
  for (const [path, n] of perFile) {
    if (!n) continue;
    out.set(path, (out.get(path) ?? 0) + n);
    let i = path.lastIndexOf("/");
    while (i > 0) {
      const d = path.slice(0, i);
      out.set(d, (out.get(d) ?? 0) + n);
      i = d.lastIndexOf("/");
    }
  }
  return out;
}
