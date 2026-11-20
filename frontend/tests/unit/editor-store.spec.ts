import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises } from "@vue/test-utils";

class FakeModel {
  alt = 1;
  listeners: (() => void)[] = [];
  constructor(
    public value: string,
    public language: string,
    public uri: { toString(): string },
  ) {}
  getValue() {
    return this.value;
  }
  setEOL() {}
  getAlternativeVersionId() {
    return this.alt;
  }
  onDidChangeContent(cb: () => void) {
    this.listeners.push(cb);
    return {
      dispose: () => (this.listeners = this.listeners.filter((l) => l !== cb)),
    };
  }
  getFullModelRange() {
    return {};
  }
  pushEditOperations(_a: unknown, edits: { text: string }[]) {
    this.type(edits[0].text);
  }
  pushStackElement() {}
  type(text: string) {
    this.value = text;
    this.alt++;
    this.listeners.forEach((l) => l());
  }
  disposed = false;
  dispose() {
    this.disposed = true;
  }
}
const fakeMonaco = {
  Uri: { file: (p: string) => ({ toString: () => `file://${p}` }) },
  editor: {
    createModel: (v: string, l: string, u: { toString(): string }) =>
      new FakeModel(v, l, u),
    EndOfLineSequence: { LF: 0, CRLF: 1 },
  },
};

vi.mock("../../src/features/editor/monaco", async (orig) => ({
  ...(await orig<object>()),
  loadMonaco: () => Promise.resolve(fakeMonaco),
}));
const api = vi.hoisted(() => ({
  projectFolder: vi.fn(),
  openWorktree: vi.fn(),
  tree: vi.fn(),
  files: vi.fn(),
  read: vi.fn(),
  write: vi.fn(),
  mkdir: vi.fn(),
  rename: vi.fn(),
}));
vi.mock("../../src/features/editor/fs", async (orig) => ({
  ...(await orig<object>()),
  fsApi: api,
}));
vi.mock("../../src/features/editor/control", async (orig) => ({
  ...(await orig<object>()),
  ControlSocket: class {
    connect() {}
    setWatch() {}
    close() {}
  },
}));

import { FsError } from "../../src/features/editor/fs";
import { useEditor } from "../../src/features/editor/editorStore";
import {
  readFolder,
  readProjectFolders,
} from "../../src/features/editor/openFiles";
import { useSession } from "../../src/stores/session";

const folder = {
  id: "f1",
  path: "/work/atlas",
  kind: "project" as const,
  project_id: "atlas",
  branch: "main",
};
const file = (content: string, hash = "h1") => ({
  content,
  hash,
  mtime: "t",
  size: content.length,
  eol: "LF" as const,
  bom: false,
  readonly: false,
});

const mem = new Map<string, string>();
beforeEach(() => {
  setActivePinia(createPinia());
  useSession().user = { id: "u1", email: "", name: "", role: "ENGINEER" };
  mem.clear();
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => mem.get(k) ?? null,
    setItem: (k: string, v: string) => void mem.set(k, v),
    removeItem: (k: string) => void mem.delete(k),
    key: (i: number) => [...mem.keys()][i] ?? null,
    get length() {
      return mem.size;
    },
  });
  Object.values(api).forEach((m) => m.mockReset());
  api.projectFolder.mockResolvedValue({ folder });
  api.tree.mockResolvedValue({
    entries: [
      { name: "main.go", kind: "file", size: 1, mtime: "", ignored: false },
    ],
  });
  api.read.mockResolvedValue(file("package main"));
  api.write.mockResolvedValue({ hash: "h2", mtime: "t2" });
});

function freshStore() {
  setActivePinia(createPinia());
  useSession().user = { id: "u1", email: "", name: "", role: "ENGINEER" };
  return useEditor();
}

async function open() {
  const s = useEditor();
  s.attach("atlas");
  const fid = await s.openProject("atlas");
  s.currentId = fid;
  await s.openFile(fid, "main.go");
  await flushPromises();
  return { s, fid, model: s.modelOf(fid, "main.go") as unknown as FakeModel };
}

describe("editor store", () => {
  it("opens a file into a model keyed by its file:// URI", async () => {
    const { s, model } = await open();
    expect(model.value).toBe("package main");
    expect(s.models.has("file:///work/atlas/main.go")).toBe(true);
    expect(s.locate("file:///work/atlas/main.go")).toEqual({
      folderId: "f1",
      path: "main.go",
    });
  });

  it("marks dirty on edit, saves with the base hash and goes clean", async () => {
    const { s, fid, model } = await open();
    model.type("package main // x");
    expect(s.folders[fid].buffers["main.go"].dirty).toBe(true);
    expect(s.folders[fid].tabs[0].preview).toBe(false);
    expect(await s.save(fid, "main.go")).toBe(true);
    expect(api.write).toHaveBeenCalledWith(fid, {
      path: "main.go",
      content: "package main // x",
      base_hash: "h1",
    });
    const b = s.folders[fid].buffers["main.go"];
    expect(b.dirty).toBe(false);
    expect(b.baseHash).toBe("h2");
  });

  it("undoing back to the saved text is clean again", async () => {
    const { s, fid, model } = await open();
    model.type("changed");
    const saved = model.alt;
    model.alt = 1;
    model.listeners.forEach((l) => l());
    expect(s.folders[fid].buffers["main.go"].dirty).toBe(false);
    expect(saved).toBe(2);
  });

  it("opens the conflict banner on 409 and writes nothing more", async () => {
    const { s, fid, model } = await open();
    model.type("mine");
    api.write.mockRejectedValue(
      new FsError(409, "CONFLICT", "changed", {
        current_hash: "disk",
        mtime: "t9",
        actor: { role: "builder", label: "Builder · wo_1" },
      }),
    );
    expect(await s.save(fid, "main.go")).toBe(false);
    const b = s.folders[fid].buffers["main.go"];
    expect(b.banner?.kind).toBe("conflict");
    expect(b.banner?.hash).toBe("disk");
    expect(b.banner?.actor?.label).toBe("Builder · wo_1");
    expect(b.dirty).toBe(true);
  });

  it("Keep mine adopts the disk hash so the next save overwrites on purpose", async () => {
    const { s, fid, model } = await open();
    model.type("mine");
    api.write.mockRejectedValueOnce(
      new FsError(409, "CONFLICT", "c", { current_hash: "disk" }),
    );
    await s.save(fid, "main.go");
    s.keepMine(fid, "main.go");
    expect(s.folders[fid].buffers["main.go"].banner).toBeNull();
    await s.save(fid, "main.go");
    expect(api.write).toHaveBeenLastCalledWith(
      fid,
      expect.objectContaining({ base_hash: "disk" }),
    );
  });

  it("Reload discards my edits and loads the disk text", async () => {
    const { s, fid, model } = await open();
    model.type("mine");
    api.read.mockResolvedValue(file("from disk", "h5"));
    await s.reloadBuffer(fid, "main.go");
    expect(model.value).toBe("from disk");
    const b = s.folders[fid].buffers["main.go"];
    expect(b.dirty).toBe(false);
    expect(b.baseHash).toBe("h5");
  });

  it("reloads a clean buffer in place when the disk changes, and shows the banner for a dirty one", async () => {
    const { s, fid, model } = await open();
    api.read.mockResolvedValue(file("external", "h7"));
    s.onFrame(fid, {
      type: "file_changed",
      path: "main.go",
      kind: "changed",
      hash: "h7",
      mtime: "t",
      actor: { role: "builder", label: "Builder · wo_1" },
    });
    await flushPromises();
    expect(model.value).toBe("external");
    expect(s.folders[fid].buffers["main.go"].banner).toBeNull();

    model.type("mine");
    s.onFrame(fid, {
      type: "file_changed",
      path: "main.go",
      kind: "changed",
      hash: "h8",
      mtime: "t",
      actor: null,
    });
    await flushPromises();
    expect(model.value).toBe("mine");
    expect(s.folders[fid].buffers["main.go"].banner?.kind).toBe("changed");
  });

  it("ignores its own save echoed by the watcher", async () => {
    const { s, fid, model } = await open();
    model.type("mine");
    await s.save(fid, "main.go");
    s.onFrame(fid, {
      type: "file_changed",
      path: "main.go",
      kind: "changed",
      hash: "h2",
      mtime: "t",
      actor: null,
    });
    await flushPromises();
    expect(s.folders[fid].buffers["main.go"].banner).toBeNull();
  });

  it("a deleted file offers recreate, which saves with create", async () => {
    const { s, fid } = await open();
    s.onFrame(fid, {
      type: "file_changed",
      path: "main.go",
      kind: "deleted",
      hash: "",
      mtime: "",
      actor: null,
    });
    expect(s.folders[fid].buffers["main.go"].banner?.kind).toBe("deleted");
    s.keepMine(fid, "main.go");
    await s.save(fid, "main.go");
    expect(api.write).toHaveBeenLastCalledWith(
      fid,
      expect.objectContaining({ base_hash: "", create: true }),
    );
  });

  it("413 and 415 become placeholders without a model", async () => {
    const { s, fid } = await open();
    api.read.mockRejectedValueOnce(
      new FsError(415, "BINARY", "binary", { size: 10 }),
    );
    await s.openFile(fid, "logo.png");
    expect(s.folders[fid].buffers["logo.png"].status).toBe("placeholder");
    expect(s.folders[fid].buffers["logo.png"].placeholder?.code).toBe(415);
    expect(s.modelOf(fid, "logo.png")).toBeUndefined();
  });

  it("a folder_gone frame marks the folder gone and forgets its persisted tabs", async () => {
    const { s, fid } = await open();
    s.persistNow(fid);
    expect(readFolder("u1", fid)).not.toBeNull();
    s.onFrame(fid, { type: "folder_gone" });
    expect(s.folders[fid].status).toBe("gone");
    expect(readFolder("u1", fid)).toBeNull();
  });

  it("replaces the preview tab and pins on double open", async () => {
    const { s, fid } = await open();
    api.read.mockResolvedValue(file("b"));
    await s.openFile(fid, "b.go");
    expect(s.folders[fid].tabs.map((t) => t.path)).toEqual(["b.go"]);
    await s.openFile(fid, "b.go", { pin: true });
    await s.openFile(fid, "c.go");
    expect(s.folders[fid].tabs.map((t) => t.path)).toEqual(["b.go", "c.go"]);
  });

  it("closing a dirty tab asks first; discard disposes the model and drops the tab", async () => {
    const { s, fid, model } = await open();
    model.type("mine");
    s.requestClose(fid, "main.go");
    expect(s.closeRequest).toEqual({ folderId: fid, paths: ["main.go"] });
    await s.answerClose("discard");
    expect(s.folders[fid].tabs).toHaveLength(0);
    expect(model.disposed).toBe(true);
    expect(s.models.size).toBe(0);
  });

  it("leaving with dirty buffers asks; cancel keeps them, discard lets go", async () => {
    const { s, model } = await open();
    expect(await s.confirmLeave()).toBe(true);
    model.type("mine");
    const p = s.confirmLeave();
    expect(s.leavePrompt).not.toBeNull();
    await s.answerLeave("cancel");
    expect(await p).toBe(false);
    const q = s.confirmLeave();
    await s.answerLeave("discard");
    expect(await q).toBe(true);
  });

  it("persists open tabs per user and folder, and the rail reads them without Monaco", async () => {
    const { s, fid } = await open();
    await s.openFile(fid, "b.go", { pin: true });
    s.persistNow(fid);
    const raw = JSON.parse(mem.get(`legos-editor:u1:${fid}`)!);
    expect(raw.tabs.map((t: { path: string }) => t.path)).toEqual(
      ["main.go", "b.go"].filter((p) =>
        raw.tabs.some((t: { path: string }) => t.path === p),
      ),
    );
    expect(raw.active).toBe("b.go");
    expect(readProjectFolders("u1", "atlas")).toHaveLength(1);
    expect(readProjectFolders("u2", "atlas")).toHaveLength(0);
    expect(readProjectFolders("u1", "other")).toHaveLength(0);

    const fresh = freshStore();
    fresh.attach("atlas");
    expect(fresh.folders[fid].status).toBe("dormant");
    expect(fresh.folders[fid].tabs.map((t) => t.path)).toContain("b.go");
    expect(fresh.monaco).toBeNull();
  });

  it("restoring drops a tab whose file no longer exists", async () => {
    const { s, fid } = await open();
    await s.openFile(fid, "gone.go", { pin: true });
    s.persistNow(fid);
    const fresh = freshStore();
    api.read.mockImplementation((_f: string, p: string) =>
      p === "gone.go"
        ? Promise.reject(new FsError(404, "NOT_FOUND", "x", {}))
        : Promise.resolve(file("x")),
    );
    fresh.attach("atlas");
    await fresh.openProject("atlas");
    await flushPromises();
    expect(fresh.folders[fid].tabs.map((t) => t.path)).not.toContain("gone.go");
  });

  it("keeps buffers when the selection switches (folders are page-scoped) and clears on detach", async () => {
    const { s, fid, model } = await open();
    model.type("mine");
    expect(s.folders[fid].buffers["main.go"].dirty).toBe(true);
    expect(s.dirtyPaths()).toEqual([{ folderId: fid, path: "main.go" }]);
    s.detach();
    expect(Object.keys(s.folders)).toHaveLength(0);
    expect(model.disposed).toBe(true);
  });

  it("ignores a stale read for a buffer that was released and reopened", async () => {
    const { s, fid } = await open();
    let first!: (v: unknown) => void;
    let second!: (v: unknown) => void;
    let n = 0;
    api.read.mockImplementation((_f: string, p: string) =>
      p !== "other.go"
        ? Promise.resolve(file("x"))
        : new Promise((r) => (n++ ? (second = r) : (first = r))),
    );
    void s.openFile(fid, "other.go");
    await flushPromises();
    void s.openFile(fid, "x.go");
    void s.openFile(fid, "other.go");
    await flushPromises();
    first(file("old", "h-old"));
    await flushPromises();
    expect(s.folders[fid].buffers["other.go"].status).toBe("loading");
    expect(s.modelOf(fid, "other.go")).toBeUndefined();
    second(file("new", "h-new"));
    await flushPromises();
    const m = s.modelOf(fid, "other.go") as unknown as FakeModel;
    expect(m.value).toBe("new");
    expect(m.disposed).toBe(false);
    expect(s.folders[fid].buffers["other.go"].baseHash).toBe("h-new");
  });

  it("opens a worktree folder through the API and registers it", async () => {
    const s = useEditor();
    s.attach("atlas");
    api.openWorktree.mockResolvedValue({
      folder: {
        id: "w1",
        path: "/data/worktrees/atlas/wo_1",
        kind: "worktree",
        wo_id: "wo_1",
        project_id: "atlas",
      },
    });
    const f = await s.openWorktree("wo_1");
    expect(f.id).toBe("w1");
    expect(s.folders.w1.status).toBe("ready");
    expect(api.openWorktree).toHaveBeenCalledWith("wo_1", undefined);
  });
});
