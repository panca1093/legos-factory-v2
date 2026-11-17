// Typed API contracts mirroring the backend JSON responses.

export interface User {
  id: string;
  email: string;
  name: string;
  role: "OWNER" | "ADMIN" | "ENGINEER" | "VIEWER";
}

export interface Project {
  id: string;
  workspace_id?: string;
  name: string;
  description: string;
  repo_url: string;
  language: string;
  git_connection_id?: string;
  remote_repo?: string;
  publish_via?: "connection" | "gh" | "none";
  github_repo?: string;
  kb_include?: string[];
  kb_exclude?: string[];
  created_at: string;
}

export interface CheckStep {
  name: string;
  command: string;
  dir?: string;
  optional?: boolean;
  source?: string;
  per_run?: boolean;
}

export interface SmokeProcess {
  name: string;
  dir?: string;
  command: string;
  port_env: string;
  ready: string;
  env?: Record<string, string>;
}

export interface CheckSmoke {
  start: SmokeProcess[];
  base: string;
  login?: { path: string; user_env: string; password_env: string };
  routes_from: string;
  blocking?: boolean;
  params?: Record<string, string>;
  ready_timeout_sec?: number;
}

export interface CheckProfile {
  version?: number;
  language?: string;
  sources?: { path: string; sha256: string }[];
  services?: { compose: string; names: string[]; ports?: Record<string, number> };
  database?: { service: string; admin_url: string };
  env?: Record<string, string>;
  setup?: CheckStep[];
  checks: CheckStep[];
  smoke?: CheckSmoke | null;
}

export type CheckOutcome = "PASS" | "FAIL" | "UNVERIFIED";

export interface CheckDryRun {
  name: string;
  kind: "setup" | "check";
  command: string;
  outcome: CheckOutcome;
  exit_code: number | null;
  output_tail: string;
}

export interface CheckProposal {
  profile: CheckProfile;
  results: CheckDryRun[];
  notes?: Record<string, string>;
  base_sha: string;
  model: string;
  created_at: string;
}

export interface ChecksStatus {
  profile: CheckProfile | null;
  proposal: CheckProposal | null;
  run: {
    status: "idle" | "running" | "ready" | "failed";
    error?: string;
    updated_at?: string;
  };
  stale: boolean;
}

export interface LocalInspect {
  path: string;
  name: string;
  is_git: boolean;
  github_repo: string;
  remote_url: string;
  default_branch: string;
  language: string;
  repos: LocalInspect[];
}

export interface LocalGh {
  installed: boolean;
  authenticated: boolean;
  account: string;
}

export type WorkOrderState =
  | "CREATED"
  | "REQUIREMENT_ANALYSIS"
  | "TASK_DECOMPOSITION"
  | "SPEC_AND_PLAN_GENERATION"
  | "ARCHITECT_REVIEW"
  | "WAITING_FOR_HUMAN"
  | "PLANNING"
  | "QUEUED"
  | "IMPLEMENTING"
  | "VERIFYING"
  | "SPECIALIST_REVIEW"
  | "PR_PREPARATION"
  | "MERGE_READINESS"
  | "READY"
  | "NEEDS_HUMAN_REVIEW"
  | "BLOCKED"
  | "CANCELLED";

export interface WorkOrder {
  id: string;
  project_id: string;
  title: string;
  objective: string;
  acceptance: string;
  state: WorkOrderState;
  builder_model: string;
  inspector_model: string;
  source_chat_id?: string;
  clarif_question?: string;
  clarif_answer?: string;
  revision: number;
  commit_sha?: string;
  foreman_model?: string;
  architect_model?: string;
  plan_review?: "" | "pending" | "approved";
  plan_revisions?: number;
  brief?: string;
  spec?: string;
  paused_at?: string;
  pause_reason?: string;
  source_ref?: string;
  pr_url?: string;
  pr_ref?: string;
  pr_merged_at?: string | null;
  pr_closed_at?: string | null;
  pr_track_failed_at?: string | null;
  pr_open_failed_at?: string | null;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface Task {
  id: string;
  work_order_id: string;
  seq: number;
  title: string;
  objective: string;
  role: string;
  deps: string[];
  scope?: string[];
}

export interface Run {
  id: string;
  task_id: string | null;
  kind: string;
  state: string;
  harness: string;
  output?: string;
  error?: string;
  tokens_in: number;
  tokens_out: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  cost_micros: number;
  created_at: string;
  finished_at?: string;
}

export interface Finding {
  id: string;
  run_id: string;
  severity: "blocker" | "major" | "minor" | "info";
  title: string;
  detail: string;
  location: string;
  status: "open" | "confirmed" | "disputed" | "resolved" | "escalated";
  specialty?: string;
  note?: string;
  decided_by?: string;
  created_at: string;
}

export interface QualityCheck {
  id: string;
  name: string;
  command: string;
  outcome: "PASS" | "FAIL" | "UNVERIFIED";
  exit_code: number;
  output: string;
  revision: number;
  commit_sha?: string;
  created_at: string;
}

export interface ModelInfo {
  provider_id: string;
  model_id: string;
  name: string;
  harness: string;
}

export interface UsageDay {
  date: string;
  count: number;
}
export interface UsageTotals {
  tokens_in: number;
  tokens_out: number;
  cost_micros: number;
  runs: number;
}
export interface UsageSummary {
  days: number;
  from: string;
  to: string;
  activity: UsageDay[];
  roles: (UsageTotals & { role: string })[];
  month_to_date: UsageTotals & { month: string };
}

export interface Overview {
  metrics: {
    active_work_orders: number;
    waiting_for_human: number;
    pending_jobs: number;
    failed_jobs: number;
    projects: number;
    total_work_orders: number;
  };
  usage: { cost_micros: number; tokens_in: number; tokens_out: number };
  user: User | null;
}

export type RoleKey =
  "foreman" | "architect" | "builder" | "inspector" | "chat";
export type AgentID = "claude" | "codex" | "opencode";

export interface Settings {
  foreman_model?: string;
  architect_model: string;
  builder_model: string;
  inspector_model: string;
  chat_model?: string;
  allowed_models: string[] | null;
  role_agents?: Partial<Record<RoleKey, string>>;
  role_permissions?: { chat?: "ask" | "read" };
  max_concurrent_runs?: number;
  jev_enabled?: boolean;
  jev_key_set?: boolean;
}

export type SettingsPatch = Partial<
  Pick<
    Settings,
    | "foreman_model"
    | "architect_model"
    | "builder_model"
    | "inspector_model"
    | "chat_model"
    | "role_agents"
    | "role_permissions"
    | "max_concurrent_runs"
    | "jev_enabled"
  >
> & { jev_key?: string };

export interface RuntimeInfo {
  credentials_configured: boolean;
  jev_key_from_env: boolean;
}

export interface UsageWindow {
  used_pct: number;
  resets_at?: string;
}
export interface AgentAccount {
  id: string;
  label: string;
  system: boolean;
  active: boolean;
  status: "signed_in" | "signed_out" | "unknown";
  who?: string;
  plan?: string;
  expires_at?: string;
  usage?: { session?: UsageWindow; week?: UsageWindow };
  usage_error?: string;
  login_command?: string;
}
export interface AgentInfo {
  id: string;
  name: string;
  command: string;
  installed: boolean;
  enabled: boolean;
  install_hint?: string;
  roles_allowed: RoleKey[];
  accounts: AgentAccount[];
}
export interface AgentsResponse {
  agents: AgentInfo[];
}
export interface NewAccount {
  account: AgentAccount;
  login_command: string;
  env_hint?: string;
}

export type ChatPermissionDecision = "allow" | "allow_all" | "deny";

export interface ChatPermission {
  id: string;
  server: string;
  tool: string;
  input: string;
  state: "pending" | "answering" | "allowed" | "denied" | "expired";
}

export const MCP_ROLES = [
  "foreman",
  "architect",
  "builder",
  "inspector",
  "chat",
] as const;

export interface McpTool {
  name: string;
  description: string;
  read_only: boolean;
  enabled: boolean;
  size: number;
  unusable: boolean;
  unusable_reason?: string;
}

export interface McpServer {
  id: string;
  name: string;
  transport: "stdio" | "http";
  command: string;
  args: string[];
  url: string;
  auth: "none" | "token" | "oauth";
  roles: string[];
  tools: McpTool[];
  status: "active" | "disabled" | "needs_sign_in";
  last_error: string;
  discovered_at?: string;
  created_at: string;
  updated_at: string;
  secret_keys: string[];
  approx_tokens: number;
  signed_in: boolean;
  expires_at?: string;
  oauth_client: "" | "dynamic" | "manual";
}

export type Trust = "verified" | "human_decision" | "ai_interpretation";

export interface KnowledgeRecord {
  id: string;
  workspace_id: string;
  project_id: string;
  work_order_id?: string;
  repo_url?: string;
  commit_sha?: string;
  kind: string;
  trust: Trust;
  title: string;
  body: string;
  actor: string;
  source_refs: string[];
  created_at: string;
}

export interface KBDoc {
  id: string;
  source: "repo" | "factory";
  project_id?: string;
  path: string;
  commit_sha: string;
  content_hash: string;
  type: string;
  status: "draft" | "stable" | "deprecated";
  trust: "unverified" | "machine" | "human";
  title: string;
  description: string;
  read_only?: boolean;
  meta: Record<string, unknown> & { origin?: string };
  links?: string[];
  indexed_at: number | string;
}

export interface KBDocDetail {
  doc: KBDoc;
  frontmatter: Record<string, unknown>;
  body: string;
  links: KBDoc[];
  backlinks: KBDoc[];
  editable: boolean;
  default_available: boolean;
}

export interface WOEvent {
  id: number;
  work_order_id: string;
  actor: string;
  kind: string;
  message: string;
  payload?: Record<string, unknown>;
  created_at: string;
}

export interface WODiffFile {
  path: string;
  old_path?: string;
  status: "added" | "modified" | "deleted" | "renamed";
  additions: number;
  deletions: number;
  binary: boolean;
  patch: string;
  truncated: boolean;
}

export interface WODiff {
  available: boolean;
  reason?: string;
  base?: string;
  head?: string;
  revision?: number;
  files: WODiffFile[];
  truncated?: boolean;
}

export type Provider = "github" | "gitlab" | "bitbucket" | "jira";

export interface Connection {
  id: string;
  provider: Provider;
  name: string;
  scope: "shared" | "personal";
  owner_id: string;
  base_url: string;
  username?: string;
  status: "active" | "disabled";
  account?: string;
  verified_at?: string;
  last_error?: string;
  has_credential: boolean;
  created_at: string;
  used_by?: { project_id: string; name: string }[];
}

export interface ConnectionProvider {
  id: Provider;
  group: "code" | "work";
  accounts: Connection[];
}

export interface IssueDraft {
  source_ref: string;
  title: string;
  objective: string;
  acceptance: string;
  url: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers:
      body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
    signal,
  });
  const text = await resp.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!resp.ok) {
    if (
      resp.status === 401 &&
      path !== "/api/v1/auth/session" &&
      path !== "/api/v1/auth/dev-login" &&
      typeof window !== "undefined"
    ) {
      window.dispatchEvent(new Event("factory:unauthorized"));
    }

    const err = (data as { error?: { code?: string; message?: string } })
      ?.error;
    throw new ApiError(
      resp.status,
      err?.code ?? "HTTP_ERROR",
      err?.message ?? text.slice(0, 200),
    );
  }
  return data as T;
}

export const api = {
  get: <T>(p: string, signal?: AbortSignal) =>
    request<T>("GET", p, undefined, signal),
  post: <T>(p: string, b?: unknown, signal?: AbortSignal) =>
    request<T>("POST", p, b ?? {}, signal),
  put: <T>(p: string, b?: unknown) => request<T>("PUT", p, b ?? {}),
  patch: <T>(p: string, b?: unknown) => request<T>("PATCH", p, b ?? {}),
  del: <T>(p: string) => request<T>("DELETE", p),
};
