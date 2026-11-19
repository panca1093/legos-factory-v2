import { describe, expect, it, vi } from "vitest";
import {
  dirMarker,
  fileMarker,
  flattenTree,
  markerOf,
  rollupCounts,
  rollupMarkers,
  visibleWindow,
  type TreeEntry,
} from "../../src/components/files/tree";
import { rankPaths } from "../../src/features/editor/fuzzy";
import {
  buildWatchSet,
  ControlSocket,
} from "../../src/features/editor/control";
import { conflictOf, FsError } from "../../src/features/editor/fs";

const d = (name: string): TreeEntry => ({ name, kind: "dir" });
const f = (name: string): TreeEntry => ({ name, kind: "file" });

describe("flattenTree", () => {
  const listings: Record<string, TreeEntry[]> = {
    "": [f("b.go"), d("Zed"), d("backend"), f("A.md"), d("web")],
    backend: [d("internal")],
    "backend/internal": [d("http")],
    "backend/internal/http": [f("server.go"), f("z.go")],
    web: [f("x.ts")],
    Zed: [],
  };
  const list = (dir: string) => listings[dir];

  it("sorts folders first, case-insensitively, and compacts single-child chains", () => {
    const rows = flattenTree(list, new Set());
    expect(rows.map((r) => r.label)).toEqual([
      "backend/internal/http",
      "web",
      "Zed",
      "A.md",
      "b.go",
    ]);
    expect(rows[0].path).toBe("backend");
    expect(rows[0].tail).toBe("backend/internal/http");
  });

  it("shows children of an open compacted row one level down", () => {
    const rows = flattenTree(list, new Set(["backend"]));
    expect(rows.slice(0, 3).map((r) => [r.label, r.depth])).toEqual([
      ["backend/internal/http", 0],
      ["server.go", 1],
      ["z.go", 1],
    ]);
  });

  it("does not compact a folder that holds a file next to a folder", () => {
    const rows = flattenTree(
      (x) => ({ "": [d("a")], a: [d("b"), f("c")], "a/b": [] })[x],
      new Set(),
    );
    expect(rows[0].label).toBe("a");
  });

  it("skips folders whose listing is not loaded yet", () => {
    const rows = flattenTree(
      (x) => (x === "" ? [d("lazy")] : undefined),
      new Set(["lazy"]),
    );
    expect(rows.map((r) => r.label)).toEqual(["lazy"]);
  });
});

describe("visibleWindow", () => {
  it("returns an overscanned slice", () => {
    expect(visibleWindow(1000, 240, 240, 24, 2)).toEqual({ start: 8, end: 22 });
    expect(visibleWindow(5, 0, 240)).toEqual({ start: 0, end: 5 });
  });
});

describe("git markers", () => {
  it("maps porcelain codes", () => {
    expect(markerOf("?", "?")).toBe("U");
    expect(markerOf(".", "M")).toBe("M");
    expect(markerOf("A", ".")).toBe("A");
    expect(markerOf("U", "U")).toBe("!");
    expect(markerOf("D", ".")).toBe("D");
    expect(markerOf(".", ".")).toBeNull();
  });
  it("rolls the strongest marker up to folders and covers untracked directories", () => {
    const m = rollupMarkers([
      { path: "a/b/c.go", x: ".", y: "M" },
      { path: "a/n.go", x: "?", y: "?" },
      { path: "new/", x: "?", y: "?" },
      { path: "z/x.go", x: "U", y: "U" },
    ]);
    expect(dirMarker(m, "a")).toBe("M");
    expect(dirMarker(m, "a/b")).toBe("M");
    expect(dirMarker(m, "z")).toBe("!");
    expect(fileMarker(m, "a/b/c.go")).toBe("M");
    expect(fileMarker(m, "new/deep/file.go")).toBe("U");
    expect(dirMarker(m, "new/deep")).toBe("U");
    expect(fileMarker(m, "other.go")).toBeUndefined();
  });
  it("rolls error counts up to every ancestor", () => {
    const c = rollupCounts(
      new Map([
        ["a/b/x.go", 2],
        ["a/y.go", 1],
        ["clean.go", 0],
      ]),
    );
    expect(c.get("a")).toBe(3);
    expect(c.get("a/b")).toBe(2);
    expect(c.get("a/b/x.go")).toBe(2);
    expect(c.has("clean.go")).toBe(false);
  });
});

describe("quick open ranking", () => {
  it("prefers basename and consecutive matches and drops non-subsequences", () => {
    const r = rankPaths("srv", [
      "backend/internal/http/server.go",
      "docs/serve.md",
      "x/y.go",
      "src/rv.ts",
    ]);
    expect(r).not.toContain("x/y.go");
    expect(r[0]).toBe("backend/internal/http/server.go");
  });
});

describe("watch set", () => {
  it("always keeps root and open-tab directories and drops the oldest expanded ones past the cap", () => {
    const set = buildWatchSet(["a/b/x.go", "m.go"], ["e1", "e2", "e3"], 4);
    expect(set.dirs).toEqual(["", "a/b", "e2", "e3"]);
    expect(set.files).toEqual(["a/b/x.go", "m.go"]);
    const tight = buildWatchSet(["a/x.go", "b/y.go"], ["e1"], 3);
    expect(tight.dirs).toEqual(["", "a", "b"]);
  });
});

describe("conflictOf", () => {
  it("reads the 409 stat from the body or the envelope", () => {
    const a = new FsError(409, "CONFLICT", "x", {
      current_hash: "h",
      mtime: "t",
      actor: null,
    });
    const b = new FsError(409, "CONFLICT", "x", {
      error: { code: "CONFLICT", current_hash: "h2" },
    });
    expect(conflictOf(a)?.current_hash).toBe("h");
    expect(conflictOf(b)?.current_hash).toBe("h2");
    expect(conflictOf(new FsError(500, "X", "x", {}))).toBeNull();
  });
});

describe("ControlSocket", () => {
  class FakeWS {
    static OPEN = 1;
    static instances: FakeWS[] = [];
    readyState = 0;
    sent: string[] = [];
    onopen: (() => void) | null = null;
    onclose: (() => void) | null = null;
    onmessage: ((e: { data: string }) => void) | null = null;
    onerror: (() => void) | null = null;
    constructor(public url: string) {
      FakeWS.instances.push(this);
    }
    send(s: string) {
      this.sent.push(s);
    }
    close() {
      this.onclose?.();
    }
  }

  it("sends the watch set on open, reconnects with backoff and reports reopen", () => {
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", FakeWS);
    FakeWS.instances = [];
    const frames: unknown[] = [];
    const opens: boolean[] = [];
    const sock = new ControlSocket("f1", {
      frame: (x) => frames.push(x),
      open: (again) => opens.push(again),
    });
    sock.setWatch({ dirs: ["", "a"], files: ["a/x.go"] });
    sock.connect();
    const ws = FakeWS.instances[0];
    expect(ws.url).toContain("/api/v1/editor/folders/f1/ws");
    ws.readyState = 1;
    ws.onopen?.();
    expect(JSON.parse(ws.sent[0])).toEqual({
      type: "watch",
      dirs: ["", "a"],
      files: ["a/x.go"],
    });
    ws.onmessage?.({ data: JSON.stringify({ type: "dir_changed", dir: "a" }) });
    expect(frames).toEqual([{ type: "dir_changed", dir: "a" }]);
    ws.onclose?.();
    vi.advanceTimersByTime(1000);
    expect(FakeWS.instances).toHaveLength(2);
    FakeWS.instances[1].readyState = 1;
    FakeWS.instances[1].onopen?.();
    expect(opens).toEqual([false, true]);
    sock.close();
    vi.advanceTimersByTime(60000);
    expect(FakeWS.instances).toHaveLength(2);
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
});
