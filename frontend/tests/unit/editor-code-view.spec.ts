import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const fakeMonaco = {
  Uri: { file: (p: string) => ({ toString: () => `file://${p}` }) },
  editor: {
    createModel: (v: string, _l: string, uri: { toString(): string }) => ({
      uri,
      getValue: () => v,
      setEOL() {},
      getAlternativeVersionId: () => 1,
      onDidChangeContent: () => ({ dispose() {} }),
      dispose() {},
    }),
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

import CodeView from "../../src/features/editor/CodeView.vue";
import { useEditor } from "../../src/features/editor/editorStore";
import { useSession } from "../../src/stores/session";

const folder = {
  id: "f1",
  path: "/work/atlas",
  kind: "project" as const,
  project_id: "atlas",
  branch: "main",
};

const mem = new Map<string, string>();
function newPinia() {
  setActivePinia(createPinia());
  useSession().user = { id: "u1", email: "", name: "", role: "ENGINEER" };
}

beforeEach(() => {
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
  api.tree.mockResolvedValue({ entries: [] });
  api.read.mockImplementation((_f: string, p: string) =>
    Promise.resolve({
      content: p,
      hash: "h",
      mtime: "t",
      size: 1,
      eol: "LF",
      bom: false,
      readonly: false,
    }),
  );
});

describe("CodeView", () => {
  it("opens ?file= instead of the tab restored from the last visit", async () => {
    newPinia();
    const prev = useEditor();
    prev.attach("atlas");
    const fid = await prev.openProject("atlas");
    await prev.openFile(fid, "old.go");
    prev.persistNow(fid);

    newPinia();
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/projects/:id/code", component: CodeView }],
    });
    await router.push("/projects/atlas/code?file=new.go&line=3");
    mount(CodeView, {
      global: {
        plugins: [router],
        stubs: { EditorWorkbench: true },
      },
    });
    await flushPromises();
    await flushPromises();

    const s = useEditor();
    expect(s.folders[fid].active).toBe("new.go");
    expect(router.currentRoute.value.query.file).toBe("new.go");
  });
});
