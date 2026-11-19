import { createPinia, setActivePinia } from "pinia";
import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import MonacoHost from "../../src/features/editor/MonacoHost.vue";
import { useEditor } from "../../src/features/editor/editorStore";

function fakeEditor() {
  const e = {
    model: null as unknown,
    position: null as { lineNumber: number; column: number } | null,
    revealed: 0,
    setModel(m: unknown) {
      e.model = m;
    },
    setPosition(p: { lineNumber: number; column: number }) {
      e.position = p;
    },
    revealLineInCenter(n: number) {
      e.revealed = n;
    },
    revealLineInCenterIfOutsideViewport() {},
    saveViewState: () => null,
    restoreViewState() {},
    onDidChangeCursorPosition() {},
    addCommand() {},
    updateOptions() {},
    focus() {},
    dispose() {},
  };
  return e;
}

const model = (path: string) => ({
  uri: { toString: () => `file:///w/${path}` },
  onDidChangeContent: () => ({ dispose() {} }),
});

describe("MonacoHost reveal", () => {
  it("places the cursor at the requested line once the model arrives", async () => {
    setActivePinia(createPinia());
    const ed = fakeEditor();
    const store = useEditor();
    store.monaco = {
      editor: { create: () => ed },
      KeyMod: { CtrlCmd: 1, Alt: 2 },
      KeyCode: { KeyS: 1, KeyP: 2, KeyW: 3 },
    } as never;
    vi.spyOn(store, "locate").mockImplementation((uri: string) => ({
      folderId: "f1",
      path: uri.replace("file:///w/", ""),
    }));
    const w = mount(MonacoHost, {
      props: { model: null, readOnly: false, reveal: null },
    });
    const reveal = { folderId: "f1", path: "b.ts", line: 42, col: 3, n: 1 };
    await w.setProps({ reveal });
    expect(ed.position).toBeNull();

    await w.setProps({ model: model("a.ts") as never });
    expect(ed.position).toBeNull();

    await w.setProps({ model: model("b.ts") as never });
    expect(ed.position).toEqual({ lineNumber: 42, column: 3 });
    expect(ed.revealed).toBe(42);
  });
});
