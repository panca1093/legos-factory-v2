export interface SessionPart {
  key: string;
  tokens: number;
}
export interface SessionCache {
  read: number;
  write: number;
  uncached: number;
  hit_pct: number;
  last_turn_at: string;
  warm_until: string;
}
export interface SessionRunning {
  work_order_id: string;
  title: string;
  state: string;
  role: string;
  task_id: string;
  since: string;
  workspace: string;
}
export interface SessionProject {
  id: string;
  name: string;
  local: {
    path: string;
    branch: string;
    head: string;
    dirty: number;
  } | null;
  worktrees: { path: string; branch: string; head: string; main: boolean }[];
  checkout: { sha: string; behind_local: number | null } | null;
  running: SessionRunning[];
}
export interface SessionMCP {
  id: string;
  name: string;
  transport: string;
  auth: string;
  status: string;
  offered: boolean;
  tools_enabled: number;
  tools_total: number;
  asks: boolean;
  calls: Record<string, number>;
}
export interface SessionInfo {
  project: SessionProject | null;
  context: {
    model: string;
    window: number;
    used: number;
    parts: SessionPart[];
    cache: SessionCache | null;
  };
  totals: {
    turns: number;
    tokens_in: number;
    tokens_out: number;
    cache_read: number;
    cache_write: number;
    cost_micros: number;
  };
  mcp: SessionMCP[];
}

export const partLabels: Record<string, string> = {
  agent: "Agent system prompt + tools",
  mcp: "MCP tool definitions",
  facts: "Repository facts",
  files: "Attached files",
  messages: "Messages",
};
export const partColors: Record<string, string> = {
  agent: "var(--chart-2)",
  mcp: "var(--info)",
  facts: "var(--crew-foreman)",
  files: "var(--crew-architect)",
  messages: "var(--chart-1)",
};

export function fmtTokens(n: number) {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(Math.max(0, Math.round(n)));
}

export function usedPct(used: number, window: number) {
  return window > 0 ? Math.min(100, Math.round((used / window) * 100)) : null;
}

export function partBars(parts: SessionPart[], used: number, window: number) {
  const base = window > 0 ? window : used;
  if (base <= 0) return [];
  const shown = parts.filter((p) => p.tokens > 0);
  const sum = shown.reduce((t, p) => t + p.tokens, 0);
  const scale = sum > base ? base / sum : 1;
  return shown.map((p) => ({
    key: p.key,
    label: partLabels[p.key] ?? p.key,
    tokens: p.tokens,
    pct: Math.round((p.tokens / base) * 100),
    width: (p.tokens / base) * 100 * scale,
  }));
}

export function cacheState(
  cache: SessionCache | null | undefined,
  now = Date.now(),
) {
  if (!cache) return null;
  const left = Date.parse(cache.warm_until) - now;
  return left > 0
    ? { warm: true, minutesLeft: Math.max(1, Math.ceil(left / 60000)) }
    : { warm: false, minutesLeft: 0 };
}

export function callsLine(calls: Record<string, number>) {
  return Object.entries(calls)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([tool, n]) => `${tool} ×${n}`)
    .join(" · ");
}

export const shortSha = (sha: string) => sha.slice(0, 7);

export function sinceLabel(iso: string, now = Date.now()) {
  const m = Math.floor((now - Date.parse(iso)) / 60000);
  if (!Number.isFinite(m) || m < 1) return "just now";
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  return h < 24 ? `${h} h` : `${Math.floor(h / 24)} d`;
}

const storeKey = "legos.chatRail.open";
const defaults: Record<string, boolean> = {
  project: true,
  context: true,
  mcp: false,
  task: true,
};
export function loadOpen(): Record<string, boolean> {
  try {
    const raw = JSON.parse(localStorage.getItem(storeKey) ?? "{}");
    return { ...defaults, ...(raw && typeof raw === "object" ? raw : {}) };
  } catch {
    return { ...defaults };
  }
}
export function saveOpen(open: Record<string, boolean>) {
  try {
    localStorage.setItem(storeKey, JSON.stringify(open));
  } catch {
    return;
  }
}
