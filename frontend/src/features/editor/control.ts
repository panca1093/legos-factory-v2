import type { ControlFrame } from "./types";

export const WATCH_CAP = 256;
const BACKOFF = [1000, 2000, 5000, 15000, 30000];

export interface WatchSet {
  dirs: string[];
  files: string[];
}

// Open-tab directories are never dropped; the oldest expanded ones go first.
export function buildWatchSet(
  tabPaths: string[],
  expanded: string[],
  cap = WATCH_CAP,
): WatchSet {
  const dirOf = (p: string) =>
    p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "";
  const dirs = new Set<string>([""]);
  for (const p of tabPaths) dirs.add(dirOf(p));
  const keep = Math.max(0, cap - dirs.size);
  const extra =
    keep > 0 ? expanded.filter((d) => !dirs.has(d)).slice(-keep) : [];
  for (const d of extra) dirs.add(d);
  return { dirs: [...dirs].slice(0, cap), files: [...tabPaths] };
}

export interface ControlHandlers {
  frame: (f: ControlFrame) => void;
  open?: (reconnected: boolean) => void;
  state?: (s: "connecting" | "open" | "closed") => void;
}

export function controlURL(folderID: string): string {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${location.host}/api/v1/editor/folders/${encodeURIComponent(folderID)}/ws`;
}

export class ControlSocket {
  private ws: WebSocket | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private attempt = 0;
  private closed = false;
  private hasOpened = false;
  private watch: WatchSet = { dirs: [""], files: [] };

  constructor(
    private folderID: string,
    private h: ControlHandlers,
    private make: (url: string) => WebSocket = (u) => new WebSocket(u),
  ) {}

  connect() {
    if (this.closed || this.ws) return;
    this.h.state?.("connecting");
    let ws: WebSocket;
    try {
      ws = this.make(controlURL(this.folderID));
    } catch {
      this.retry();
      return;
    }
    this.ws = ws;
    ws.onopen = () => {
      this.attempt = 0;
      this.h.state?.("open");
      const again = this.hasOpened;
      this.hasOpened = true;
      this.sendWatch();
      this.h.open?.(again);
    };
    ws.onmessage = (ev) => {
      try {
        this.h.frame(JSON.parse(String(ev.data)) as ControlFrame);
      } catch {
        /* A malformed frame is ignored. */
      }
    };
    ws.onclose = () => {
      this.ws = null;
      this.h.state?.("closed");
      this.retry();
    };
    ws.onerror = () => ws.close();
  }

  setWatch(set: WatchSet) {
    this.watch = set;
    this.sendWatch();
  }

  close() {
    this.closed = true;
    clearTimeout(this.timer);
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onclose = ws.onmessage = ws.onerror = ws.onopen = null;
      ws.close();
    }
  }

  private sendWatch() {
    if (this.ws?.readyState === WebSocket.OPEN)
      this.ws.send(JSON.stringify({ type: "watch", ...this.watch }));
  }

  private retry() {
    if (this.closed) return;
    clearTimeout(this.timer);
    const wait = BACKOFF[Math.min(this.attempt++, BACKOFF.length - 1)];
    this.timer = setTimeout(() => this.connect(), wait);
  }
}
