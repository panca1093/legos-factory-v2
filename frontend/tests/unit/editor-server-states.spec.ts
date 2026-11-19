import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";
import { useEditor } from "../../src/features/editor/editorStore";

beforeEach(() => setActivePinia(createPinia()));

describe("serverStates", () => {
  it("keeps one entry per folder and server", () => {
    const st = useEditor();
    st.setServerState("project", "gopls", { state: "running", version: "1" });
    st.setServerState("worktree", "gopls", { state: "found" });
    expect(st.serverStates.project.gopls.state).toBe("running");
    expect(st.serverStates.worktree.gopls.state).toBe("found");
  });
});
