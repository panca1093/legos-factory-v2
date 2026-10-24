import type {
  Overview,
  Project,
  WorkOrder,
  Task,
  Run,
  Settings,
  QualityCheck,
  Finding,
  WOEvent,
} from "../../src/types/api";
export const user = {
  id: "usr-test",
  name: "Saifullah Panca",
  email: "saifullah.panca@amartha.com",
  role: "OWNER" as const,
};
export const projects: Project[] = [
  {
    id: "atlas",
    name: "Atlas Commerce",
    repo_url: "atlas/storefront",
    language: "typescript",
    description: "",
    created_at: "2026-10-24T01:00:00Z",
  },
  {
    id: "loan",
    name: "Loan Platform",
    repo_url: "demo/loan-service",
    language: "go",
    description: "",
    created_at: "2026-10-24T01:00:00Z",
  },
  {
    id: "orbit",
    name: "Orbit SDK",
    repo_url: "orbit/client-sdk",
    language: "rust",
    description: "",
    created_at: "2026-10-24T01:00:00Z",
  },
];
const order = (
  id: string,
  project_id: string,
  title: string,
  state: WorkOrder["state"],
): WorkOrder => ({
  id,
  project_id,
  title,
  state,
  objective:
    "Prevent duplicate ledger entries when a payment request is retried after a timeout. Preserve the response contract.",
  acceptance:
    "Repeated requests with the same key produce one ledger entry. Conflicting payloads return a clear conflict. Existing clients keep working.",
  autonomy: "assisted",
  builder_model: "test/builder",
  inspector_model: "test/inspector",
  revision: 1,
  created_by: user.id,
  created_at: "2026-10-24T01:00:00Z",
  updated_at: "2026-10-24T01:00:00Z",
});
export const orders = [
  order(
    "LF-128",
    "loan",
    "Make payment retries idempotent",
    "WAITING_FOR_HUMAN",
  ),
  order("LF-127", "atlas", "Add accessible product search", "IMPLEMENTING"),
  order("LF-125", "atlas", "Ship team invitation flow", "NEEDS_HUMAN_REVIEW"),
  order("LF-124", "orbit", "Handle connection backpressure", "CREATED"),
  order("LF-121", "loan", "Validate settlement timestamps", "READY"),
];
orders[0].clarif_question = "How long should an idempotency key remain valid?";
export const overview: Overview = {
  user,
  metrics: {
    active_work_orders: 2,
    waiting_for_human: 1,
    pending_jobs: 1,
    failed_jobs: 0,
    projects: 3,
    total_work_orders: 5,
  },
  usage: { cost_micros: 18400000, tokens_in: 12000, tokens_out: 4000 },
  budget: {
    limit_micros: 40000000,
    spent_micros: 18400000,
    reserved_micros: 0,
    available_micros: 21600000,
  },
};
export const settings: Settings = {
  budget_limit_micros: 40000000,
  architect_model: "test/architect",
  builder_model: "test/builder",
  inspector_model: "test/inspector",
  allowed_models: null,
};
export const models = ["architect", "builder", "inspector"].map((role) => ({
  provider_id: "test",
  model_id: role,
  name: role[0].toUpperCase() + role.slice(1) + " model",
  harness: "mock",
}));
export const tasks: Task[] = [
  {
    id: "baseline",
    work_order_id: "LF-127",
    seq: 1,
    title: "Record failing baseline",
    objective: "Prove the acceptance test fails before implementation.",
    role: "builder",
    deps: [],
  },
  {
    id: "implementation",
    work_order_id: "LF-127",
    seq: 2,
    title: "Scoped implementation",
    objective: "Implement the approved contract.",
    role: "builder",
    deps: ["baseline"],
  },
  {
    id: "tests",
    work_order_id: "LF-127",
    seq: 3,
    title: "Independent contract tests",
    objective: "Exercise retry, conflict and compatibility behavior.",
    role: "builder",
    deps: ["baseline"],
  },
  {
    id: "inspection",
    work_order_id: "LF-127",
    seq: 4,
    title: "Inspector review",
    objective: "Inspect the final revision and evidence.",
    role: "inspector",
    deps: ["implementation", "tests"],
  },
];
export const runs: Run[] = [
  {
    id: "run-1",
    task_id: "baseline",
    kind: "builder",
    state: "completed",
    harness: "mock",
    output: "Baseline: acceptance check failed as expected.",
    tokens_in: 100,
    tokens_out: 200,
    cost_micros: 0,
    created_at: "2026-10-24T01:20:00Z",
  },
  {
    id: "run-2",
    task_id: "implementation",
    kind: "builder",
    state: "running",
    harness: "mock",
    output: "Implementing the acceptance contract.",
    tokens_in: 100,
    tokens_out: 200,
    cost_micros: 0,
    created_at: "2026-10-24T01:25:00Z",
  },
];
export const checks: QualityCheck[] = [
  {
    id: "check-0",
    name: "Contract baseline",
    command: "go test ./...",
    outcome: "FAIL",
    exit_code: 1,
    output: "Expected baseline failure: greeter.Greet missing",
    revision: 0,
    created_at: "2026-10-24T01:20:00Z",
  },
  {
    id: "check-1",
    name: "Acceptance tests",
    command: "go test ./...",
    outcome: "PASS",
    exit_code: 0,
    output: "ok factory-fixture/greeter_test",
    revision: 1,
    created_at: "2026-10-24T01:30:00Z",
  },
];
export const findings: Finding[] = [
  {
    id: "finding-1",
    run_id: "review-1",
    severity: "blocker",
    title: "Retry behavior needs a regression test",
    detail: "The initial revision did not exercise conflicting payloads.",
    location: "internal/payment/retry_test.go:42",
    status: "resolved",
    created_at: "2026-10-24T01:29:00Z",
  },
];
export const events: WOEvent[] = [
  {
    id: 1,
    work_order_id: "LF-127",
    actor: "architect",
    kind: "architect_approved",
    message: "Approved acceptance mapping and work breakdown.",
    created_at: "2026-10-24T01:20:00Z",
  },
  {
    id: 2,
    work_order_id: "LF-127",
    actor: "builder",
    kind: "agent_started",
    message: "Recorded a failing baseline. Starting scoped implementation.",
    created_at: "2026-10-24T01:25:00Z",
  },
];
