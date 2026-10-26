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
  created_at: string;
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
  autonomy: "guided" | "assisted" | "delegated";
  builder_model: string;
  inspector_model: string;
  clarif_question?: string;
  clarif_answer?: string;
  revision: number;
  commit_sha?: string;
  foreman_model?: string;
  architect_model?: string;
  plan_review?: "" | "pending" | "approved";
  review_rounds?: number;
  paused_at?: string;
  pause_reason?: string;
  source_ref?: string;
  pr_url?: string;
  pr_ref?: string;
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
  budget: {
    limit_micros: number;
    spent_micros: number;
    reserved_micros: number;
    available_micros: number;
  };
  user: User | null;
}

export interface Settings {
  budget_limit_micros: number;
  foreman_model?: string;
  architect_model: string;
  builder_model: string;
  inspector_model: string;
  allowed_models: string[] | null;
  cataloger_summaries?: boolean;
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

export interface WOEvent {
  id: number;
  work_order_id: string;
  actor: string;
  kind: string;
  message: string;
  payload?: Record<string, unknown>;
  created_at: string;
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
): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers:
      body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
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
  get: <T>(p: string) => request<T>("GET", p),
  post: <T>(p: string, b?: unknown) => request<T>("POST", p, b ?? {}),
  put: <T>(p: string, b?: unknown) => request<T>("PUT", p, b ?? {}),
  patch: <T>(p: string, b?: unknown) => request<T>("PATCH", p, b ?? {}),
  del: <T>(p: string) => request<T>("DELETE", p),
};
