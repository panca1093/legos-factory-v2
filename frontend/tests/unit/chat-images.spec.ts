import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { createPinia, setActivePinia } from "pinia";
import { describe, expect, it, vi } from "vitest";
import { addFiles, detectImage } from "../../src/features/chats/chatUtils";
import ChatComposer from "../../src/features/chats/ChatComposer.vue";
import ChatsView from "../../src/features/chats/ChatsView.vue";
import { useSession } from "../../src/stores/session";
import { useChats } from "../../src/stores/chats";

const PNG = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4];
const JPG = [0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4];
const GIF = [0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 1, 2];
const WEBP = [0x52, 0x49, 0x46, 0x46, 9, 9, 9, 9, 0x57, 0x45, 0x42, 0x50, 1];
const WAV = [0x52, 0x49, 0x46, 0x46, 9, 9, 9, 9, 0x57, 0x41, 0x56, 0x45, 1];
const file = (name: string, bytes: number[] | number) =>
  new File(
    [
      typeof bytes === "number"
        ? new Uint8Array(bytes).fill(0x61)
        : new Uint8Array(bytes),
    ],
    name,
  );
const imageFile = (name: string, size: number) => {
  const b = new Uint8Array(size);
  b.set(PNG);
  return new File([b], name);
};
const b64 = (bytes: number[]) => btoa(String.fromCharCode(...bytes));

describe("detects images", () => {
  it("detects all four types and rejects RIFF that is not WEBP", () => {
    expect(detectImage(new Uint8Array(PNG))).toBe("image/png");
    expect(detectImage(new Uint8Array(JPG))).toBe("image/jpeg");
    expect(detectImage(new Uint8Array(GIF))).toBe("image/gif");
    expect(detectImage(new Uint8Array(WEBP))).toBe("image/webp");
    expect(detectImage(new Uint8Array(WAV))).toBeUndefined();
  });
  it("encodes images as base64, magic bytes beat the extension", async () => {
    const r = await addFiles(
      [],
      [
        file("a.png", PNG),
        file("b.jpeg", JPG),
        file("c.gif", GIF),
        file("d.webp", WEBP),
        file("mislabelled.txt", PNG),
      ],
    );
    expect(r.errors).toEqual([]);
    expect(r.files.map((f) => f.mediaType)).toEqual([
      "image/png",
      "image/jpeg",
      "image/gif",
      "image/webp",
      "image/png",
    ]);
    expect(r.files[0]).toMatchObject({
      encoding: "base64",
      kind: "image",
      content: b64(PNG),
      size: PNG.length,
    });
    expect(r.files[0].preview).toBe(`data:image/png;base64,${b64(PNG)}`);
  });
  it("rejects image extensions without a signature and keeps plain text as text", async () => {
    const r = await addFiles([], [file("fake.png", 20), file("a.txt", 5)]);
    expect(r.errors).toEqual([
      "fake.png is not a supported image (PNG, JPEG, GIF or WebP).",
    ]);
    expect(r.files).toHaveLength(1);
    expect(r.files[0].encoding).toBeUndefined();
    const w = await addFiles([], [file("x.png", WAV)]);
    expect(w.errors[0]).toContain("not a supported image");
  });
});

describe("zip rejected", () => {
  it("keeps the UTF-8 text error for a .zip", async () => {
    const r = await addFiles([], [file("a.zip", [0x50, 0x4b, 3, 4, 0xff, 0])]);
    expect(r.errors).toEqual(["a.zip is not a UTF-8 text file."]);
    expect(r.files).toEqual([]);
  });
});

describe("image size caps", () => {
  it("accepts 5,242,880 base64 characters and rejects one more", async () => {
    const ok = await addFiles([], [imageFile("ok.png", 3_932_160)]);
    expect(ok.errors).toEqual([]);
    expect(ok.files[0].content.length).toBe(5_242_880);
    const big = await addFiles([], [imageFile("big.png", 3_932_161)]);
    expect(big.errors).toEqual(["big.png is over 3.75 MB."]);
    expect(big.files).toEqual([]);
  });
  it("rejects the image that crosses the 10,485,760 total", async () => {
    const r = await addFiles(
      [],
      [
        imageFile("a.png", 3_932_160),
        imageFile("b.png", 3_932_160),
        imageFile("c.png", 12),
      ],
    );
    expect(r.files.map((f) => f.name)).toEqual(["a.png", "b.png"]);
    expect(r.errors).toEqual(["Images can total 7.5 MB at most."]);
  });
  it("counts images only toward image caps and text only toward text caps", async () => {
    const r = await addFiles(
      [],
      [
        imageFile("a.png", 3_932_160),
        file("a", 100 * 1024),
        file("b", 100 * 1024),
        file("c", 100 * 1024),
      ],
    );
    expect(r.errors).toEqual([]);
    expect(r.files).toHaveLength(4);
  });
  it("limits text and images together to 5 attachments", async () => {
    const r = await addFiles(
      [],
      [
        ...Array.from({ length: 5 }, (_, i) => file(`t${i}`, 3)),
        file("a.png", PNG),
      ],
    );
    expect(r.files).toHaveLength(5);
    expect(r.errors.join()).toContain("5 files");
  });
});

describe("image chip", () => {
  const models = [{ id: "", name: "Auto" }];
  it("shows a thumbnail chip with name, size and a working remove button, and sends it", async () => {
    const w = mount(ChatComposer, { props: { models } });
    const input = w.find('input[type="file"]');
    Object.defineProperty(input.element, "files", {
      value: [file("shot.png", PNG)],
    });
    await input.trigger("change");
    await vi.waitFor(() => expect(w.find(".ch-chip").exists()).toBe(true));
    const chip = w.find(".ch-chip");
    expect(chip.find("img.ch-thumb").attributes("src")).toContain(
      "data:image/png;base64,",
    );
    expect(chip.text()).toContain("shot.png");
    expect(chip.text()).toContain("12 B");
    await chip.find("button").trigger("click");
    expect(w.find(".ch-chip").exists()).toBe(false);
  });
});

describe("image send payload", () => {
  it("sends the PNG with encoding base64 through ChatsView and shows an image attachment", async () => {
    setActivePinia(createPinia());
    useSession().user = { role: "OWNER" } as never;
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
    try {
      const w = mount(ChatsView, { global: { plugins: [router] } });
      await flushPromises();
      const input = w.find('input[type="file"]');
      Object.defineProperty(input.element, "files", {
        value: [file("shot.png", PNG)],
      });
      await input.trigger("change");
      await vi.waitFor(() => expect(w.find(".ch-chip").exists()).toBe(true));
      await w.find("textarea").setValue("what is this");
      await w.find("button.ch-send").trigger("click");
      const call = await vi.waitFor(() => {
        const c = fetchMock.mock.calls.find((x) => x[1]?.method === "POST");
        expect(c).toBeTruthy();
        return c!;
      });
      const body = JSON.parse(call[1]!.body as string);
      expect(body.files).toEqual([
        { name: "shot.png", content: b64(PNG), encoding: "base64" },
      ]);
      expect(
        Uint8Array.from(atob(body.files[0].content), (c) => c.charCodeAt(0)),
      ).toEqual(new Uint8Array(PNG));
      expect(useChats().messages.c[0].attachments).toEqual([
        { kind: "image", name: "shot.png", size: PNG.length },
      ]);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
