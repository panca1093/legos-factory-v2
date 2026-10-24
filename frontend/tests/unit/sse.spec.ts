import { describe, expect, it, vi } from "vitest";
import { subscribe } from "../../src/services/events/sse";

describe("sse", () => {
  it("subscribes and returns an unsubscribe that closes the source", async () => {
    // minimal EventSource stub
    let closed = false;
    class FakeEventSource {
      static instances: FakeEventSource[] = [];
      url: string;
      onmessage: ((ev: unknown) => void) | null = null;
      onerror: ((ev: unknown) => void) | null = null;
      listeners = new Map<string, (ev: unknown) => void>();
      constructor(url: string, _opts?: unknown) {
        this.url = url;
        FakeEventSource.instances.push(this);
      }
      addEventListener(kind: string, cb: (ev: unknown) => void) {
        this.listeners.set(kind, cb);
      }
      close() {
        closed = true;
      }
    }
    // @ts-ignore test stub lacks full typing
    vi.stubGlobal("EventSource", FakeEventSource);

    const events: unknown[] = [];
    const stop = subscribe("/api/v1/events", (ev) => events.push(ev));
    const es = FakeEventSource.instances.at(-1)!;
    expect(es.url).toBe("/api/v1/events");

    // simulate a named event frame
    es.listeners.get("state_changed")?.({
      data: JSON.stringify({ id: 1, kind: "state_changed", message: "hi" }),
    });
    expect(events).toHaveLength(1);

    stop();
    expect(closed).toBe(true);
  });
});
