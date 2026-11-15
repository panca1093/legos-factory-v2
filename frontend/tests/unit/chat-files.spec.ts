import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  MSG_OFFICE,
  MSG_OTHER,
  classifyFile,
  formatSize,
  precheckFile,
} from "../../src/features/chats/chatUtils";
import AttachmentChip from "../../src/features/chats/AttachmentChip.vue";
import ChatComposer from "../../src/features/chats/ChatComposer.vue";
import ChatsView from "../../src/features/chats/ChatsView.vue";
import { useChats } from "../../src/stores/chats";
import { useSession } from "../../src/stores/session";

const MB = 1024 * 1024;
const f = (name: string, size = 10, type = "", lastModified = 1) =>
  new File([new Uint8Array(size)], name, { type, lastModified });

describe("precheckFile", () => {
  it("classifies by extension and MIME", () => {
    expect(classifyFile({ name: "a.PNG", type: "" })).toBe("image");
    expect(classifyFile({ name: "x", type: "image/webp" })).toBe("image");
    expect(classifyFile({ name: "a.pdf", type: "" })).toBe("pdf");
    expect(classifyFile({ name: "a.docx", type: "" })).toBe("office");
    expect(classifyFile({ name: "a.heic", type: "image/heic" })).toBe("other");
    expect(classifyFile({ name: "a.zip", type: "application/zip" })).toBe(
      "other",
    );
    expect(classifyFile({ name: "main.go", type: "" })).toBe("text");
    expect(classifyFile({ name: "Makefile", type: "" })).toBe("text");
  });
  it("uses the server wording for office and unsupported files", () => {
    expect(precheckFile(f("a.xlsx"), [])).toEqual({ error: MSG_OFFICE });
    expect(precheckFile(f("a.mp4"), [])).toEqual({ error: MSG_OTHER });
    expect(MSG_OFFICE).toBe(
      "Word, Excel and PowerPoint files are not supported yet. Export as PDF or text.",
    );
    expect(MSG_OTHER).toBe(
      "Only images (PNG, JPEG, GIF, WebP), PDF and text files can be attached.",
    );
  });
  it("enforces per-kind limits at the boundary", () => {
    const ok = (name: string, size: number) =>
      "kind" in precheckFile({ name, type: "", size }, []);
    expect(ok("a.png", 3_932_160)).toBe(true);
    expect(ok("a.png", 3_932_161)).toBe(false);
    expect(ok("a.pdf", 18 * MB)).toBe(true);
    expect(ok("a.pdf", 18 * MB + 1)).toBe(false);
    expect(ok("a.txt", MB)).toBe(true);
    expect(ok("a.txt", MB + 1)).toBe(false);
    expect(precheckFile({ name: "e.txt", type: "", size: 0 }, [])).toEqual({
      error: "e.txt is empty.",
    });
  });
  it("words size errors exactly", () => {
    const err = (name: string, size: number) =>
      (precheckFile({ name, type: "", size }, []) as { error: string }).error;
    expect(err("a.png", 3_932_161)).toBe("a.png is over 3.75 MB.");
    expect(err("a.pdf", 18 * MB + 1)).toBe("a.pdf is over 18 MB.");
    expect(err("a.txt", MB + 1)).toBe("a.txt is over 1 MB.");
  });
  it("enforces 10 files and 25 MB per message", () => {
    const ten = Array.from({ length: 10 }, () => ({ size: 1 }));
    expect(precheckFile(f("a.txt"), ten)).toEqual({
      error: "Up to 10 files per message.",
    });
    const r = precheckFile({ name: "b.pdf", type: "", size: 4 * MB }, [
      { size: 15 * MB },
    ]);
    expect(r).toEqual({ error: "Attachments can total 18 MB at most." });
  });
  it("formats sizes", () => {
    expect(formatSize(12)).toBe("12 B");
    expect(formatSize(2048)).toBe("2.0 KB");
    expect(formatSize(300 * 1024)).toBe("300 KB");
    expect(formatSize(3_932_160)).toBe("3.75 MB");
    expect(formatSize(18 * MB)).toBe("18 MB");
  });
});

class FakeXHR {
  static all: FakeXHR[] = [];
  upload: { onprogress?: (e: unknown) => void } = {};
  onload?: () => void;
  onerror?: () => void;
  onabort?: () => void;
  status = 0;
  responseText = "";
  withCredentials = false;
  aborted = false;
  url = "";
  body?: FormData;
  constructor() {
    FakeXHR.all.push(this);
  }
  open(_m: string, url: string) {
    this.url = url;
  }
  send(b: FormData) {
    this.body = b;
  }
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
  progress(loaded: number, total: number) {
    this.upload.onprogress?.({ lengthComputable: true, loaded, total });
  }
  reply(status: number, data: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(data);
    this.onload?.();
  }
}
const info = (id: string, name: string, kind = "text", size = 10) => ({
  id,
  name,
  media_type: "text/plain",
  kind,
  size,
});

describe("composer upload chips", () => {
  const models = [{ id: "", name: "Auto" }];
  const fetchMock = vi.fn(async () => ({
    ok: true,
    status: 204,
    text: async () => "",
  }));
  beforeEach(() => {
    FakeXHR.all = [];
    fetchMock.mockClear();
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());
  const mountComposer = (props: Record<string, unknown> = {}) =>
    mount(ChatComposer, { props: { models, chatId: "c1", ...props } });
  const pick = async (w: ReturnType<typeof mountComposer>, ...fs: File[]) => {
    const input = w.find('input[type="file"]');
    Object.defineProperty(input.element, "files", {
      value: fs,
      configurable: true,
    });
    await input.trigger("change");
    await flushPromises();
  };
  const sendBtn = (w: ReturnType<typeof mountComposer>) =>
    w.find("button.ch-send");

  it("shows progress, completes, and posts with credentials to the chat", async () => {
    const w = mountComposer();
    await pick(w, f("a.txt"));
    const x = FakeXHR.all[0];
    expect(x.url).toBe("/api/v1/chats/c1/files");
    expect(x.withCredentials).toBe(true);
    expect((x.body!.get("file") as File).name).toBe("a.txt");
    x.progress(5, 10);
    await flushPromises();
    expect(w.find(".ch-chip").text()).toContain("50%");
    expect(w.find('[role="progressbar"]').attributes("aria-valuenow")).toBe(
      "50",
    );
    x.reply(200, info("cf_1", "a.txt"));
    await flushPromises();
    expect(w.find(".ch-chip").attributes("data-status")).toBe("done");
    expect(w.find(".ch-chip").text()).toContain("10 B");
  });

  it("disables send while uploading and names the file, then enables", async () => {
    const w = mountComposer();
    await w.find("textarea").setValue("hi");
    await pick(w, f("slow.pdf", 10, "application/pdf"));
    expect(sendBtn(w).attributes("disabled")).toBeDefined();
    expect(w.find(".ch-hint").text()).toBe("Uploading slow.pdf…");
    FakeXHR.all[0].reply(200, info("cf_1", "slow.pdf", "pdf"));
    await flushPromises();
    expect(sendBtn(w).attributes("disabled")).toBeUndefined();
    await sendBtn(w).trigger("click");
    const sent = w.emitted("send")![0][0] as {
      content: string;
      files: { id: string; kind: string }[];
    };
    expect(sent.content).toBe("hi");
    expect(sent.files.map((x) => [x.id, x.kind])).toEqual([["cf_1", "pdf"]]);
    expect(w.find(".ch-chips").exists()).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows the busy-queue error on the chip and strips an invalid request prefix", async () => {
    const w = mountComposer();
    await pick(w, f("a.txt"));
    FakeXHR.all[0].reply(409, {
      error: {
        code: "X",
        message:
          "invalid request: Too many files waiting to be sent in this chat. Send or remove some first.",
      },
    });
    await flushPromises();
    expect(w.find(".ch-chip").text()).toContain(
      "Too many files waiting to be sent in this chat. Send or remove some first.",
    );
    expect(w.find(".ch-chip").text()).not.toContain("invalid request");
  });

  it("keeps send disabled after a failed upload, with the file named, until removed", async () => {
    const w = mountComposer();
    await w.find("textarea").setValue("hi");
    await pick(w, f("bad.txt"));
    FakeXHR.all[0].reply(400, {
      error: { code: "BAD", message: MSG_OTHER },
    });
    await flushPromises();
    const chip = w.find(".ch-chip");
    expect(chip.attributes("data-status")).toBe("error");
    expect(chip.text()).toContain(MSG_OTHER);
    expect(sendBtn(w).attributes("disabled")).toBeDefined();
    expect(w.find(".ch-hint").text()).toBe(
      "Upload failed for bad.txt. Remove it to send.",
    );
    await chip.find("button").trigger("click");
    expect(sendBtn(w).attributes("disabled")).toBeUndefined();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("aborts the upload when a chip is removed mid-flight, without a DELETE", async () => {
    const w = mountComposer();
    await pick(w, f("a.txt"));
    const label = w.find('button[aria-label="Remove a.txt"]');
    expect(label.exists()).toBe(true);
    await label.trigger("click");
    expect(FakeXHR.all[0].aborted).toBe(true);
    expect(w.find(".ch-chip").exists()).toBe(false);
    FakeXHR.all[0].reply(200, info("cf_1", "a.txt"));
    await flushPromises();
    expect(w.find(".ch-chip").exists()).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("DELETEs an uploaded, unsent file on remove", async () => {
    const w = mountComposer();
    await pick(w, f("a.txt"));
    FakeXHR.all[0].reply(200, info("cf_9", "a.txt"));
    await flushPromises();
    await w.find('button[aria-label="Remove a.txt"]').trigger("click");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/chats/c1/files/cf_9",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  it("skips duplicate drops and files that fail the pre-check", async () => {
    const w = mountComposer();
    await pick(w, f("a.txt", 10, "", 5), f("a.txt", 10, "", 5), f("x.docx"));
    expect(w.findAll(".ch-chip")).toHaveLength(1);
    expect(FakeXHR.all).toHaveLength(1);
    expect(w.emitted("error")![0][0]).toContain("already attached");
    expect(w.emitted("error")![0][0]).toContain(MSG_OFFICE);
  });

  it("clears chips, aborts and deletes when the chat changes", async () => {
    const w = mountComposer();
    await pick(w, f("a.txt"), f("b.txt", 11));
    FakeXHR.all[0].reply(200, info("cf_1", "a.txt"));
    await flushPromises();
    await w.setProps({ chatId: "c2" });
    expect(w.find(".ch-chips").exists()).toBe(false);
    expect(FakeXHR.all[1].aborted).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/chats/c1/files/cf_1",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  it("creates the chat on first attach and keeps the chips when the route moves to it", async () => {
    const ensureChat = vi.fn(async () => "new1");
    const w = mountComposer({ chatId: "", ensureChat });
    await pick(w, f("a.txt"));
    await w.setProps({ chatId: "new1" });
    expect(ensureChat).toHaveBeenCalledTimes(1);
    expect(FakeXHR.all[0].url).toBe("/api/v1/chats/new1/files");
    expect(w.findAll(".ch-chip")).toHaveLength(1);
  });

  it("revokes object URLs on remove, send and unmount", async () => {
    const create = vi.fn(() => "blob:x");
    const revokeFn = vi.fn();
    vi.stubGlobal(
      "URL",
      Object.assign(URL, {
        createObjectURL: create,
        revokeObjectURL: revokeFn,
      }),
    );
    const w = mountComposer();
    await w.find("textarea").setValue("hi");
    await pick(w, f("a.png", 10, "image/png"), f("b.png", 12, "image/png"));
    expect(w.findAll("img.ch-thumb")).toHaveLength(2);
    await w.find('button[aria-label="Remove a.png"]').trigger("click");
    expect(revokeFn).toHaveBeenCalledTimes(1);
    FakeXHR.all[1].reply(200, info("cf_2", "b.png", "image"));
    await flushPromises();
    await sendBtn(w).trigger("click");
    expect(revokeFn).toHaveBeenCalledTimes(2);
    await pick(w, f("c.png", 13, "image/png"));
    w.unmount();
    expect(revokeFn).toHaveBeenCalledTimes(3);
  });

  it("attaches pasted images but leaves text paste alone", async () => {
    const w = mountComposer();
    const paste = async (data: Record<string, unknown>) => {
      const ev = new Event("paste", { cancelable: true, bubbles: true });
      Object.defineProperty(ev, "clipboardData", { value: data });
      w.find("textarea").element.dispatchEvent(ev);
      await flushPromises();
      return ev;
    };
    const text = await paste({
      files: [f("image.png", 10, "image/png")],
      getData: () => "copied cells",
    });
    expect(text.defaultPrevented).toBe(false);
    expect(FakeXHR.all).toHaveLength(0);
    const plain = await paste({ files: [], getData: () => "hello" });
    expect(plain.defaultPrevented).toBe(false);
    const img = await paste({
      files: [f("image.png", 10, "image/png")],
      getData: () => "",
    });
    expect(img.defaultPrevented).toBe(true);
    expect(FakeXHR.all).toHaveLength(1);
    expect(w.find(".ch-fn").text()).toMatch(/^pasted-\d+-1\.png$/);
  });

  it("ignores a restore that belongs to another chat", async () => {
    const w = mountComposer({ chatId: "c2" });
    const vm = w.vm as unknown as {
      restore: (o: string, c: string, f: unknown[]) => void;
    };
    const file = f("a.txt");
    vm.restore("c1", "old draft", [
      {
        key: "k",
        name: "a.txt",
        size: 10,
        kind: "text",
        status: "done",
        progress: 1,
        id: "cf_1",
        file,
      },
    ]);
    await flushPromises();
    expect((w.find("textarea").element as HTMLTextAreaElement).value).toBe("");
    expect(w.find(".ch-chips").exists()).toBe(false);
    vm.restore("c2", "mine", []);
    await flushPromises();
    expect((w.find("textarea").element as HTMLTextAreaElement).value).toBe(
      "mine",
    );
  });

  it("restore with no files keeps current chips and a non-empty draft", async () => {
    const w = mountComposer();
    const vm = w.vm as unknown as {
      restore: (o: string, c: string, f: unknown[]) => void;
    };
    await w.find("textarea").setValue("typing");
    await pick(w, f("a.txt"));
    FakeXHR.all[0].reply(200, info("cf_1", "a.txt"));
    await flushPromises();
    vm.restore("c1", "retry text", []);
    await flushPromises();
    expect((w.find("textarea").element as HTMLTextAreaElement).value).toBe(
      "typing",
    );
    expect(w.findAll(".ch-chip")).toHaveLength(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("keeps the remove button reachable by keyboard and labelled", async () => {
    const w = mountComposer();
    await pick(w, f("a.txt"));
    const b = w.find(".ch-chip button");
    expect(b.attributes("type")).toBe("button");
    expect(b.attributes("aria-label")).toBe("Remove a.txt");
    expect(w.find('[aria-label="Attach files"]').element.tagName).toBe(
      "BUTTON",
    );
  });
});

describe("attachment chips", () => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/:p(.*)*", component: { template: "<div />" } }],
  });
  const chip = (a: unknown, thumb = false) =>
    mount(AttachmentChip, {
      props: { a: a as never, chatId: "c1", thumb },
      global: { plugins: [router] },
    });
  it("renders image thumbnails and file chips as links to the file", () => {
    const img = chip(
      { kind: "image", name: "p.png", size: 5, file_id: "cf_1" },
      true,
    );
    expect(img.find("a").attributes("href")).toBe(
      "/api/v1/chats/c1/files/cf_1",
    );
    expect(img.find("a").attributes("target")).toBe("_blank");
    expect(img.find("img").attributes("src")).toBe(
      "/api/v1/chats/c1/files/cf_1",
    );
    const pdf = chip({
      kind: "pdf",
      name: "d.pdf",
      size: 2048,
      file_id: "cf_2",
    });
    expect(pdf.find("a.ch-chip").attributes("href")).toBe(
      "/api/v1/chats/c1/files/cf_2",
    );
    expect(pdf.text()).toContain("2.0 KB");
  });
  it("still renders old file rows and kb attachments", () => {
    const old = chip({ kind: "file", name: "old.txt", size: 12 });
    expect(old.find("a").exists()).toBe(false);
    expect(old.text()).toContain("old.txt");
    const kb = chip({ kind: "kb", name: "ADR 1", doc_id: "d1" });
    expect(kb.find("a.kb").exists()).toBe(true);
  });
});

describe("send with uploaded files", () => {
  it("sends file_ids only and shows the attachments before the reply", async () => {
    setActivePinia(createPinia());
    useSession().user = { role: "OWNER" } as never;
    FakeXHR.all = [];
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/chats/:id?", component: ChatsView }],
    });
    await router.push("/chats/c");
    const json = (data: unknown) => ({
      ok: true,
      status: 200,
      headers: new Headers({ "content-type": "application/json" }),
      json: async () => data,
      text: async () => JSON.stringify(data),
    });
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === "POST")
        return {
          ok: true,
          body: new ReadableStream({
            start(c) {
              c.enqueue(
                new TextEncoder().encode(
                  'event: error\ndata: {"error":"stop"}\n\n',
                ),
              );
              c.close();
            },
          }),
        };
      if (url.endsWith("/api/v1/chats"))
        return json({
          chats: [{ id: "c", title: "t", model: "", updated_at: "" }],
        });
      if (url.endsWith("/api/v1/chat/models")) return json({ models: [] });
      return json({ messages: [] });
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
    try {
      const w = mount(ChatsView, { global: { plugins: [router] } });
      await flushPromises();
      const input = w.find('input[type="file"]');
      Object.defineProperty(input.element, "files", {
        value: [f("shot.png", 12, "image/png")],
      });
      await input.trigger("change");
      await flushPromises();
      FakeXHR.all[0].reply(200, info("cf_7", "shot.png", "image", 12));
      await flushPromises();
      await w.find("textarea").setValue("what is this");
      await w.find("button.ch-send").trigger("click");
      const call = await vi.waitFor(() => {
        const c = fetchMock.mock.calls.find((x) => x[1]?.method === "POST");
        expect(c).toBeTruthy();
        return c!;
      });
      expect(JSON.parse(call[1]!.body as string)).toEqual({
        content: "what is this",
        file_ids: ["cf_7"],
      });
      expect(useChats().messages.c[0].attachments).toEqual([
        { kind: "image", name: "shot.png", size: 12, file_id: "cf_7" },
      ]);
      expect(w.find(".atts img").attributes("src")).toBe(
        "/api/v1/chats/c/files/cf_7",
      );
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
