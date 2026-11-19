import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ControlSocket } from "../../src/features/editor/control";

class FakeWS {
  static OPEN = 1;
  static all: FakeWS[] = [];
  readyState = 1;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    FakeWS.all.push(this);
  }
  send() {}
  close() {
    this.onclose?.();
  }
}

beforeEach(() => {
  FakeWS.all = [];
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeWS);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function sock(frames: unknown[] = []) {
  const s = new ControlSocket(
    "f1",
    { frame: (f) => frames.push(f) },
    (u) => new FakeWS(u) as unknown as WebSocket,
  );
  s.connect();
  return s;
}

describe("ControlSocket reconnect", () => {
  it("backs off and reconnects after an ordinary close", async () => {
    sock();
    FakeWS.all[0].onopen?.();
    FakeWS.all[0].onclose?.();
    await vi.advanceTimersByTimeAsync(1100);
    expect(FakeWS.all).toHaveLength(2);
  });

  it("stops after folder_gone", async () => {
    sock();
    const ws = FakeWS.all[0];
    ws.onopen?.();
    ws.onmessage?.({ data: JSON.stringify({ type: "folder_gone" }) });
    ws.onclose?.();
    await vi.advanceTimersByTimeAsync(60000);
    expect(FakeWS.all).toHaveLength(1);
  });

  it("stops when the failed upgrade is a 410", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ status: 410 }));
    const frames: unknown[] = [];
    sock(frames);
    FakeWS.all[0].onclose?.();
    await vi.advanceTimersByTimeAsync(60000);
    expect(FakeWS.all).toHaveLength(1);
    expect(frames).toEqual([{ type: "folder_gone" }]);
  });

  it("keeps retrying when the failed upgrade is a 502", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ status: 502 }));
    sock();
    FakeWS.all[0].onclose?.();
    await vi.advanceTimersByTimeAsync(1100);
    expect(FakeWS.all).toHaveLength(2);
  });
});
