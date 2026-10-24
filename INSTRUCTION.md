# Mission: Build legos-factory — Autonomous Software Engineering Factory

You are acting as the **Principal Software Architect, Engineering Lead, and Implementation Owner** for `legos-factory`.

Your mission is to transform the existing project documentation and interactive UI prototype into a **functional, maintainable, extensible autonomous software engineering platform**.

This is an **implementation task**, not a documentation-only or planning-only task.

Work autonomously through repository discovery, planning, implementation, integration, testing, review, and validation.

**Deliver a working end-to-end vertical slice first, then complete as much of the remaining MVP scope as possible.**

Do not stop after scaffolding or producing an implementation plan.

---

# 1. Repository Discovery — Mandatory First Step

Before making changes:

1. Read `AGENTS.md`, `README.md`, and all relevant PRDs, ADRs, architecture, implementation plans, UX specifications, and Markdown documentation.
2. Inspect the existing frontend prototype, assets, animations, interactions, and project dependencies.
3. Inspect Git status and preserve existing user changes.
4. Identify what is already implemented, mocked, partially implemented, or missing.
5. Identify existing build, test, and development commands.
6. Identify conflicting or outdated requirements.
7. Determine the implementation order and establish a lightweight internal task plan.

**The existing Codex-generated prototype is the visual and interaction design baseline.**

Preserve its visual identity, LEGO-inspired personas, animations, layout, typography, and user experience.

If the prototype uses another frontend framework, migrate its behavior into the mandatory Vue stack defined below.

### Requirement precedence

1. Security and repository safety requirements
2. Explicit architectural and technology decisions in this prompt
3. Existing accepted ADRs
4. PRD and acceptance criteria
5. UX specification and working prototype
6. Older implementation plans

Record important conflicts and their resolutions.

---

# 2. Product Identity and Vision

Product: **legos-factory**

A standalone, multi-project, multi-user autonomous software engineering factory.

The platform converts engineering objectives into reviewed plans, executable subtasks, verified implementations, and pull requests with explainable quality decisions.

## 2.1 Agent Personas

| Persona | Responsibility |
|---|---|
| **Foreman** | Orchestration, workflow coordination, task scheduling, delegation |
| **Architect** | Requirement analysis, task decomposition, technical planning, specification review |
| **Builder** | Coding, implementation, refactoring, test implementation |
| **Inspector** | Independent review, testing, security, UI/UX, specialized verification |
| **Cataloger** | Change history, engineering knowledge, and future second-brain capabilities |

These are logical roles, not necessarily separate services or processes.

One coding harness may execute different personas using distinct sessions, instructions, contexts, and selected models.

Cataloger's complete second-brain functionality is **future scope**. Provide only the necessary extension point and UI placeholder in the MVP.

## 2.2 Core Engineering Principles

- Simplicity over speculative abstraction.
- Modular monolith over distributed microservices.
- Hybrid code-driven and agent-driven orchestration.
- Deterministic workflow transitions.
- Agent reasoning for planning, technical decisions, and implementation.
- Architect review before implementation.
- Independent verification of agent output.
- Progressive autonomy with human escalation.
- User-controlled model selection within workspace policy.
- Cost-aware scheduling and bounded execution.
- Language-agnostic quality profiles.
- Explicit and traceable execution outcomes.

**Agents propose. The application controls. Verification determines readiness.**

---

# 3. Technology Stack — Mandatory

## 3.1 Backend

| Concern | Technology |
|---|---|
| Language | **Go** |
| HTTP Framework | **Echo** |
| API Protocol | REST JSON |
| Realtime Communication | Server-Sent Events (SSE) |
| Persistence | SQLite with WAL |
| Database Access | `database/sql` + explicit SQL |
| Database Migrations | Goose |
| Validation | Typed request DTOs + explicit validation |
| Logging | `log/slog` |
| Authentication | OIDC + secure application sessions |
| Authorization | Workspace/project RBAC |
| Workflow | Persistent state machine |
| Queue | SQLite-backed job queue |
| Concurrency | Goroutines, contexts, controlled worker pools |
| Agent Runtime | OpenCode HTTP API |
| Sandbox | Docker + isolated workspace |
| Git | Native Git + provider APIs |
| Testing | Go testing, `httptest`, integration tests, race detector |
| Deployment | Single-host, self-hosted MVP |

Use a stable, maintained Echo release compatible with the chosen Go version.

Do not introduce Gin, Fiber, Chi, or a custom HTTP router.

Echo is the mandatory HTTP framework.

Use `net/http` primitives where Echo itself depends on them, such as HTTP request contexts, response streaming, and standard test utilities.

### SQLite

Use a maintained SQLite driver. Prefer a pure-Go implementation when CGO-free deployment is beneficial.

Configure:

- WAL mode
- Foreign key enforcement
- Busy timeout
- Appropriate connection pool limits
- Safe transaction boundaries
- Schema migrations
- Database backup procedure

SQLite remains the primary database for the single-host MVP.

Do not introduce PostgreSQL unless an actual limitation is demonstrated.

## 3.2 Frontend

| Concern | Technology |
|---|---|
| Framework | **Vue 3 — Composition API** |
| Language | **TypeScript — strict mode** |
| Build Tool | Vite |
| Router | Vue Router |
| State Management | Pinia |
| Styling | Tailwind CSS + CSS variables |
| Icons | Lucide Vue |
| Animation | Vue Transition / TransitionGroup + CSS |
| API Client | Typed REST client |
| Realtime Updates | Native EventSource / SSE |
| Unit Testing | Vitest |
| Component Testing | Vue Test Utils |
| E2E Testing | Playwright |

Use Vue Single File Components with:

```vue
<script setup lang="ts">
```

Do not introduce React, Next.js, Nuxt, or another frontend framework.

Avoid heavy UI frameworks unless the existing prototype has a demonstrated requirement.

## 3.3 Deployment Architecture

Use:

- One Go backend application
- One SQLite database
- One Vue frontend
- Controlled sandbox execution processes/containers

The backend, scheduler, orchestration engine, and worker controller must run within the same Go application.

Do not introduce:

- Temporal
- Kafka
- Redis
- Kubernetes
- Microservices
- Distributed queues
- Event sourcing
- CQRS frameworks

Unless a concrete requirement makes them necessary.

For development, run Vite and Go independently with a development proxy.

For production, prefer building Vue into static assets served by the Go application, including SPA fallback routing.

---

# 4. Backend Architecture

Follow **pragmatic modular monolith architecture**.

Do not implement excessive Clean Architecture boilerplate.

## 4.1 Suggested Backend Structure

```text
backend/
├── cmd/
│   └── factory/
│       └── main.go
│
├── internal/
│   ├── app/
│   │   ├── app.go
│   │   └── bootstrap.go
│   │
│   ├── config/
│   │   └── config.go
│   │
│   ├── http/
│   │   ├── server.go
│   │   ├── routes.go
│   │   ├── response.go
│   │   ├── handlers/
│   │   │   ├── project.go
│   │   │   ├── workorder.go
│   │   │   ├── agent.go
│   │   │   ├── review.go
│   │   │   └── settings.go
│   │   ├── middleware/
│   │   │   ├── auth.go
│   │   │   └── rbac.go
│   │   └── sse/
│   │       └── stream.go
│   │
│   ├── project/
│   │   ├── model.go
│   │   ├── service.go
│   │   └── repository.go
│   │
│   ├── workorder/
│   │   ├── model.go
│   │   ├── service.go
│   │   ├── repository.go
│   │   └── transitions.go
│   │
│   ├── planning/
│   │   ├── planner.go
│   │   ├── architect.go
│   │   ├── contract.go
│   │   └── dag.go
│   │
│   ├── orchestration/
│   │   ├── foreman.go
│   │   ├── workflow.go
│   │   └── policy.go
│   │
│   ├── scheduler/
│   │   ├── scheduler.go
│   │   ├── queue.go
│   │   └── limits.go
│   │
│   ├── agents/
│   │   ├── harness.go
│   │   ├── models.go
│   │   ├── opencode/
│   │   └── mock/
│   │
│   ├── execution/
│   │   ├── runner.go
│   │   ├── workspace.go
│   │   └── sandbox/
│   │
│   ├── quality/
│   │   ├── profile.go
│   │   ├── checks.go
│   │   ├── review.go
│   │   ├── evidence.go
│   │   └── evaluator.go
│   │
│   ├── integrations/
│   │   ├── git/
│   │   │   ├── provider.go
│   │   │   ├── github/
│   │   │   ├── gitlab/
│   │   │   └── bitbucket/
│   │   └── issues/
│   │       └── jira/
│   │
│   ├── identity/
│   │   ├── auth.go
│   │   ├── session.go
│   │   └── permissions.go
│   │
│   ├── usage/
│   │   ├── tracker.go
│   │   └── budget.go
│   │
│   └── store/
│       ├── sqlite/
│       └── migrations/
│
├── tests/
│   └── integration/
│
├── go.mod
└── go.sum
```

This is the target organization, not a requirement to create every package immediately.

**Create files and packages only when implementing real behavior.**

## 4.2 Dependency Rules

Conceptual dependency direction:

```text
Echo Handler
     |
     v
Application Service
     |
     +--> Domain / Workflow Logic
     |
     +--> Persistence
     |
     +--> External Adapters
```

Rules:

- Echo handlers must remain thin.
- Business logic must not live inside HTTP handlers.
- Domain services must not depend on `echo.Context`.
- Pass `context.Context` into service methods.
- Keep persistence logic inside appropriate store/repository implementations.
- Keep provider-specific behavior inside adapters.
- Avoid cyclic dependencies.
- Prefer concrete types for internal logic.
- Introduce interfaces only when a meaningful boundary requires them.

Use interfaces for boundaries such as:

- Agent harness
- Git provider
- Issue provider
- Sandbox execution
- External dependencies that need substitution in tests

Avoid interfaces for every struct merely to follow a design pattern.

---

# 5. Backend Coding Standards

## 5.1 Go Conventions

Follow idiomatic Go:

- Explicit error handling.
- Context propagation.
- Small, cohesive functions.
- Clear ownership of goroutines.
- Explicit cancellation and timeout handling.
- Safe concurrent state access.
- Minimal global mutable state.
- Dependency injection through constructors where useful.
- Structured logging.
- No unnecessary reflection or generic frameworks.

Use `errors.Is`, `errors.As`, and `%w` for error classification and propagation.

Avoid panic for expected failures.

## 5.2 Echo HTTP Standards

Use Echo for:

- Routing
- Route groups
- Request binding
- Validation integration
- Middleware
- HTTP error handling
- Response serialization
- SSE streaming

Use versioned routes under:

```text
/api/v1
```

Implement:

- Request ID middleware
- Recovery middleware
- Authentication
- Authorization
- Structured request logging
- Request body limits
- Appropriate timeouts
- Secure CORS configuration where needed

Use centralized HTTP error handling.

Do not expose raw internal errors, stack traces, credentials, or sensitive system details.

Example API error:

```json
{
  "error": {
    "code": "INVALID_STATE_TRANSITION",
    "message": "Work order cannot start before plan approval"
  },
  "request_id": "req_example"
}
```

All public API request/response contracts must be typed and validated.

Maintain an OpenAPI specification for implemented endpoints, and use it to keep frontend types consistent.

## 5.3 API Organization

Suggested routes:

```text
GET    /health
GET    /ready

GET    /api/v1/projects
POST   /api/v1/projects
GET    /api/v1/projects/:id

GET    /api/v1/work-orders
POST   /api/v1/work-orders
GET    /api/v1/work-orders/:id

GET    /api/v1/work-orders/:id/plan
POST   /api/v1/work-orders/:id/plan/generate
POST   /api/v1/work-orders/:id/plan/decisions

POST   /api/v1/work-orders/:id/start
POST   /api/v1/work-orders/:id/cancel
POST   /api/v1/work-orders/:id/clarifications

GET    /api/v1/work-orders/:id/runs
GET    /api/v1/work-orders/:id/reviews
GET    /api/v1/work-orders/:id/quality
GET    /api/v1/work-orders/:id/events

GET    /api/v1/models
GET    /api/v1/usage

GET    /api/v1/settings/integrations
GET    /api/v1/settings/agents
GET    /api/v1/settings/quality
```

Refine route names when necessary for consistency.

Support pagination, filtering, and predictable HTTP status codes.

Operations that create external side effects must support safe retries and idempotency.

## 5.4 Background Jobs and Lifecycle

All long-running operations must have explicit lifecycle ownership.

Use contexts for cancellation and deadlines.

On shutdown:

- Stop claiming new jobs.
- Gracefully stop or checkpoint active operations where possible.
- Reconcile execution state.
- Release resources.
- Close database connections safely.

Do not start untracked goroutines that survive beyond application lifecycle management.

---

# 6. Frontend Architecture

Use a feature-based Vue architecture.

## 6.1 Suggested Frontend Structure

```text
frontend/
├── src/
│   ├── app/
│   │   ├── App.vue
│   │   ├── router/
│   │   └── layouts/
│   │
│   ├── components/
│   │   ├── ui/
│   │   ├── agents/
│   │   └── common/
│   │
│   ├── features/
│   │   ├── overview/
│   │   ├── factory/
│   │   │   ├── work-orders/
│   │   │   ├── task-graph/
│   │   │   ├── runs/
│   │   │   └── reviews/
│   │   ├── knowledge/
│   │   └── settings/
│   │       ├── models/
│   │       ├── integrations/
│   │       ├── team/
│   │       └── budgets/
│   │
│   ├── composables/
│   ├── stores/
│   ├── services/
│   │   ├── api/
│   │   └── events/
│   ├── types/
│   ├── styles/
│   │   ├── tokens.css
│   │   └── global.css
│   └── main.ts
│
├── tests/
│   ├── unit/
│   ├── components/
│   └── e2e/
│
├── package.json
└── vite.config.ts
```

## 6.2 Frontend Engineering Standards

- Vue 3 Composition API.
- `<script setup lang="ts">`.
- Strict TypeScript.
- Composables for reusable logic.
- Pinia for shared client state.
- Typed API contracts.
- Separation of UI components from API/business logic.
- Reusable design tokens.
- Accessible components.
- Responsive layouts.
- Loading, error, empty, and disabled states.
- Reduced-motion support.
- No unnecessary UI dependencies.

Use Vue transitions and CSS animations for micro-interactions.

Avoid excessive animation that hurts readability or performance.

## 6.3 Prototype Migration

Before rewriting:

1. Inspect all prototype views and interactions.
2. Identify design tokens and reusable visual components.
3. Map existing UI elements into Vue components.
4. Preserve persona visuals, animations, layout, and styling.
5. Connect UI views to real backend APIs incrementally.
6. Validate desktop and responsive behavior.
7. Execute browser E2E tests.

Do not redesign the interface unnecessarily.

**The objective is to make the existing approved design functional.**

---

# 7. Hybrid Agent Orchestration

The Go application controls workflow lifecycle.

Agent reasoning determines implementation details, task decomposition, architectural recommendations, and specialist review findings.

## 7.1 Work Order Lifecycle

```text
WORK_ORDER_CREATED
    |
    v
REQUIREMENT_ANALYSIS
    |
    v
TASK_DECOMPOSITION
    |
    v
SPEC_AND_PLAN_GENERATION
    |
    v
ARCHITECT_REVIEW
    |
    +-- REVISE --> PLANNING
    |
    +-- CLARIFY --> WAITING_FOR_HUMAN
    |                    |
    |                    v
    |                 PLANNING
    |
    v
QUEUED
    |
    v
IMPLEMENTING
    |
    v
VERIFYING
    |
    v
SPECIALIST_REVIEW
    |
    +-- FIX_REQUIRED --> IMPLEMENTING
    |
    +-- ESCALATE --> WAITING_FOR_HUMAN
    |
    v
PR_PREPARATION
    |
    v
MERGE_READINESS
    |
    +-- READY
    +-- NEEDS_HUMAN_REVIEW
    +-- BLOCKED
```

Support:

- Persistent state transitions
- Explicit transition validation
- Task decomposition
- Dependency graphs
- DAG cycle detection
- Bounded parallelism
- Retry limits
- Cancellation
- Timeouts
- Human clarification
- Human approval
- Resume after restart
- Execution history
- Audit trail

## 7.2 Planning and Architect Review

The Planner decomposes a user objective into smaller tasks.

Each subtask should contain:

- Objective
- Scope
- Dependencies
- Acceptance criteria
- Expected deliverables
- Recommended agent role
- Relevant technical context

The Architect independently reviews:

- Requirement completeness
- Architecture compatibility
- Task dependencies
- Scope and assumptions
- Edge cases
- Risks
- Testing strategy
- Acceptance criteria

Architect decisions:

```text
APPROVE
REVISE
CLARIFY
```

When important ambiguity remains, request human clarification rather than inventing product requirements.

Material changes to approved scope must return to planning/review.

Use schema-validated structured agent responses.

Do not use arbitrary text parsing to control state transitions.

## 7.3 Parallel Task Execution

Allow parallel execution only when:

- Tasks have no unsatisfied dependencies.
- Workspace/resource limits allow it.
- The affected files or interfaces can be safely isolated.
- Integration strategy is defined.

Use separate workspaces or Git worktrees when appropriate.

Integrate changes in a controlled manner.

Do not allow multiple agents to modify the same workspace concurrently without coordination.

---

# 8. OpenCode Integration

OpenCode is the initial coding-agent harness.

Inspect the installed OpenCode version and supported server API.

Prefer supported HTTP APIs/SDKs instead of parsing terminal output.

The adapter should support:

- Provider/model discovery
- Session creation
- Model selection
- Prompt submission
- Execution events
- Agent output
- Usage metadata
- Cancellation
- Error handling
- Session termination

Use an interface such as:

```go
type AgentHarness interface {
    Start(ctx context.Context, req AgentRequest) (AgentSession, error)
    Cancel(ctx context.Context, sessionID string) error
    Result(ctx context.Context, sessionID string) (AgentResult, error)
}
```

Extend the interface only when actual functionality requires it.

Do not force every harness to expose unsupported capabilities.

Avoid recursive calls into the controlling OpenCode session.

## 8.1 Authentication

For local development, support the existing authenticated OpenCode installation.

Do not copy or expose provider credentials.

Never persist subscription tokens in logs, artifacts, or frontend state.

Keep personal subscription usage separate from future shared-team credentials.

Do not assume subscription credentials can legally or technically be pooled between users.

---

# 9. Model Selection and Cost Control

Users can select models for:

- Builder
- Inspector
- Specialist Inspectors

Foreman and Architect use workspace defaults configured by Admin, with per-user preferences when workspace policy permits.

Models must come from actual available OpenCode model/provider discovery.

Track usage by:

- Workspace
- Project
- User
- Work Order
- Agent role
- Provider/model
- Execution run

Distinguish:

- Actual billed API cost
- Estimated monetary cost
- Subscription usage
- Unknown quota status
- Local compute consumption

Do not invent actual billing values.

## 9.1 Execution Limits

Support:

- Maximum concurrent sandbox runs
- Per-project concurrency limits
- Maximum revision cycles
- Execution timeout
- Retry budget
- Work Order spending policy
- Workspace/user usage attribution

Implement basic budget reservation or another safe mechanism preventing parallel tasks from independently consuming the same remaining budget.

When budget or quota cannot be measured reliably, use enforceable local limits such as maximum attempts, duration, and concurrency, and report the uncertainty.

Never skip mandatory quality checks merely to save cost.

---

# 10. Scheduler and Persistent Queue

Implement a lightweight SQLite-backed scheduler.

Suggested job states:

```text
PENDING
CLAIMED
RUNNING
COMPLETED
FAILED
CANCELLED
WAITING_FOR_HUMAN
```

The scheduler must:

- Atomically claim eligible jobs.
- Respect dependency constraints.
- Enforce resource limits.
- Avoid double execution.
- Handle application restarts.
- Reconcile orphaned running jobs.
- Handle retryable versus permanent failures.
- Support graceful shutdown.

Use database transactions for atomic state changes.

Protect against concurrent job claiming.

Do not automatically replay operations with possible external side effects without reconciliation.

Keep scheduling logic independent from Echo request handlers.

---

# 11. Sandbox Execution

Use controlled and isolated execution environments.

Support:

- Isolated repository workspaces
- Git worktrees where appropriate
- CPU and memory limits
- Execution timeouts
- Controlled network access
- Execution logs
- Cleanup
- Cancellation
- Interrupted-job recovery

Agent-generated code and repository scripts must be treated as untrusted.

Do not grant sandbox processes unrestricted:

- Host filesystem access
- Personal credentials
- Docker socket access
- Production secrets
- Other project repositories

Keep trusted local development mode separate from isolated execution mode.

Do not claim a process is sandboxed unless isolation has actually been enforced.

---

# 12. Quality Engineering

Quality must not depend on agent self-assessment.

Implement a lightweight **Contract-Driven Quality System**.

## 12.1 Quality Layers

1. Project guidelines and engineering standards
2. Acceptance contracts
3. Deterministic verification
4. Independent specialist review
5. Merge readiness evaluation

## 12.2 Project Quality Profiles

Support configurable quality profiles for:

- Go
- Java
- Rust
- Node.js
- TypeScript
- Vue
- Other supported toolchains

Reuse existing project tools and conventions.

Examples:

| Stack | Verification |
|---|---|
| Go | `go test`, `go vet`, race detector |
| Java | Maven/Gradle tests and static checks |
| Rust | `cargo check`, Clippy, `cargo test` |
| TypeScript | ESLint, TypeScript, Vitest |
| Vue | ESLint, vue-tsc, Vitest, Playwright |

Do not build a universal language-analysis framework.

Execute configured quality commands and record their actual results.

## 12.3 Acceptance Contracts

Before implementation, Architect produces versioned acceptance criteria.

Implementation tests should correspond to the expected behaviors.

Inspectors should check whether test assertions meaningfully verify the acceptance criteria.

Avoid approving tests that merely assert mocks or implementation details without verifying behavior.

## 12.4 Verification Outcomes

Every quality check must produce:

```text
PASS
FAIL
UNVERIFIED
```

Never treat an unexecuted check as successful.

Attach verification results to the candidate commit SHA and relevant execution environment.

When implementation changes, rerun affected verification.

## 12.5 Specialist Inspectors

Support specialist review categories:

- Backend
- Frontend
- UI/UX
- Security
- Testing
- Architecture

Select reviewers based on change scope and risk.

Do not invoke every reviewer for every task.

Reviewer findings must include:

- Severity
- Relevant file/location when available
- Explanation
- Supporting evidence
- Suggested remediation

Allow findings to be confirmed, disputed, resolved, or escalated.

Avoid majority voting between LLM reviewers as a substitute for evidence.

## 12.6 Merge Readiness

Combine deterministic verification and review outcomes.

Possible decisions:

```text
READY
NEEDS_REVISION
NEEDS_VERIFICATION
NEEDS_HUMAN_REVIEW
BLOCKED
```

A PR cannot become READY when required checks fail or remain unverified.

Repository branch protection and required provider approvals remain authoritative.

Do not automatically merge PRs in the MVP.

---

# 13. Projects, Teams, and Authentication

Support:

- Workspaces
- Projects
- Repositories
- Members
- Role assignments
- Work Order ownership
- Human reviews and approvals

Roles:

```text
OWNER
ADMIN
ENGINEER
VIEWER
```

Implement authorization at workspace/project boundaries.

Use OIDC-based login with secure application sessions.

Support an explicitly configured local development authentication mode, without insecure production defaults.

Never trust workspace/project IDs supplied by clients without checking authorization.

## 13.1 Provider Connections

Support integration foundations for:

- GitHub
- GitLab
- Bitbucket
- Jira

Prefer native Git and provider APIs for deterministic platform operations.

MCP is optional, not required.

Provider credentials must be scoped, protected, revocable, and never exposed to frontend or unrelated sandboxes.

For unavailable external credentials, provide testable mock adapters and clearly mark the integration as unverified.

Do not push code, create real remote PRs, or modify external Jira issues without explicit authorization.

---

# 14. Product UI and User Flow

Preserve the existing legos-factory prototype design.

Primary navigation:

## Overview

Greetings and monitoring dashboard:

- Active Work Orders
- Running tasks
- Pending human decisions
- Failed jobs
- Recent execution activity
- Usage and limits

## Factory

Unified engineering workflow:

- Projects
- Work Orders
- Task details
- Execution plans
- Task dependency graph
- Agent execution
- Review findings
- Quality verification
- PR readiness

Avoid splitting these into too many disconnected navigation menus.

Use contextual tabs and drill-down views.

## Knowledge

Cataloger / second-brain preview.

Do not build a complete knowledge system yet.

## Settings

- Integrations
- Agent defaults
- Model preferences
- Quality profiles
- Members and permissions
- Resource and cost policies

## 14.1 User Journey

Implement:

1. Select project/repository.
2. Create Work Order.
3. Choose Builder and Inspector models.
4. Analyze requirements.
5. Generate implementation plan.
6. Architect reviews the plan.
7. Handle clarification if required.
8. Schedule approved subtasks.
9. Execute implementation.
10. Verify changes.
11. Review findings.
12. Perform bounded remediation.
13. Evaluate PR readiness.
14. Present results and evidence.

Use Vue and SSE to display live execution state.

Support loading, failure, reconnection, cancellation, and human waiting states.

Do not display simulated progress as real execution.

---

# 15. Testing Standards for legos-factory

Use TDD where practical and write meaningful tests for implemented behavior.

## 15.1 Backend

Required testing areas:

- Work Order state transitions
- Invalid transitions
- DAG validation
- Scheduler job claiming
- Concurrent execution limits
- Retry behavior
- Persistence and restart recovery
- Human approvals
- Authentication and authorization
- Quality policy evaluation
- Usage and budget logic
- Agent adapter failures
- HTTP request validation

Use:

- Go standard testing package
- Echo + `httptest`
- SQLite integration tests
- Race detector where practical

Avoid mocks when real deterministic local dependencies are inexpensive.

Use mocks/fakes for external providers and agent harnesses.

## 15.2 Frontend

Use:

- TypeScript type checking
- ESLint
- Vitest
- Vue Test Utils
- Playwright

Test important user journeys:

- Create project
- Create Work Order
- Select models
- Review implementation plan
- Approve/revise plan
- Request human clarification
- Monitor execution
- View quality findings
- View PR readiness
- Change settings
- Handle failed API requests

Do not rely only on snapshot tests.

## 15.3 Integration Testing

Demonstrate at least one complete local Work Order lifecycle using the mock harness.

Where available, separately verify an actual OpenCode-driven execution.

Do not present the mock flow as proof that the real OpenCode integration works.

---

# 16. Implementation Strategy

Implement incrementally within this assignment.

## Phase A — Foundation

- Inspect the existing repository.
- Establish Go + Echo backend.
- Establish Vue 3 + TypeScript frontend.
- Configure routing, state management, Tailwind, and design tokens.
- Configure SQLite and migrations.
- Implement application startup.
- Connect frontend and backend development environments.
- Establish testing and local startup commands.

## Phase B — Core Workflow

- Project management
- Work Order management
- Persistent workflow states
- Task decomposition
- DAG validation
- Plan review
- Human clarification
- SQLite scheduler
- API/frontend integration

## Phase C — OpenCode Integration

- Harness adapter
- Model discovery
- Model configuration
- Agent sessions
- Execution lifecycle
- SSE progress
- Cancellation
- Execution history

## Phase D — Quality and Review

- Project quality profiles
- Deterministic check execution
- Acceptance criteria
- Specialist Inspector review
- Remediation loops
- Quality reports
- Merge readiness evaluation

## Phase E — Collaboration and Integrations

- Workspace/project membership
- Authentication and RBAC
- Usage tracking
- Resource policies
- Git provider adapters
- Jira issue import foundation

## Phase F — End-to-End Validation

- Run the application.
- Exercise the complete Work Order lifecycle.
- Validate Vue frontend interactions.
- Run Go tests.
- Run frontend tests.
- Run lint/type checks.
- Run Playwright tests.
- Verify persistence and recovery.
- Fix discovered defects.

Prioritize a functioning end-to-end vertical slice over wide but superficial feature coverage.

Do not build placeholders that look functional without implementing their underlying behavior.

If complete MVP coverage is not achievable within the execution, finish and verify the highest-priority vertical slice and explicitly report what remains incomplete.

---

# 17. Engineering Constraints

Follow these rules:

- Preserve existing user changes.
- Keep dependencies minimal.
- Follow idiomatic Go and TypeScript.
- Prefer explicit implementation over premature abstraction.
- Avoid framework mixing.
- Avoid unnecessary background services.
- Avoid speculative scaling architecture.
- Do not introduce distributed infrastructure.
- Do not weaken tests to make them pass.
- Do not bypass quality gates.
- Do not expose credentials.
- Do not silently broaden agent permissions.
- Do not claim unverified integrations are working.
- Do not perform irreversible external actions without authorization.

Use specialist agents or delegation when they materially improve speed, implementation quality, or independent verification.

Avoid excessive agent delegation that only increases cost.

---

# 18. Definition of Done

The deliverable must contain:

- Runnable Go + Echo backend
- Runnable Vue 3 + TypeScript frontend
- SQLite database and migrations
- Documented local development setup
- Working project and Work Order lifecycle
- Persistent orchestration states
- Task decomposition and plan review
- Human clarification/resumption
- Model configuration
- OpenCode adapter or clearly documented external blocker
- Controlled scheduler and execution limits
- Quality checks and reports
- Review findings
- PR readiness decision
- Working frontend/API integration
- Automated tests
- Updated documentation

Demonstrate:

```text
Create Project
    ↓
Create Work Order
    ↓
Foreman / Architect Planning
    ↓
Architect Review
    ↓
Human Clarification (if needed)
    ↓
Approved Plan
    ↓
Task Scheduling
    ↓
Builder Implementation
    ↓
Verification
    ↓
Inspector Review
    ↓
PR Readiness Decision
```

Use a mock harness only when necessary and clearly distinguish mock execution from real execution.

---

# 19. Final Self-Review

Before finishing:

1. Review the implementation against PRD requirements.
2. Inspect the overall architecture for unnecessary complexity.
3. Run backend tests and appropriate race checks.
4. Run frontend type checking, lint, and tests.
5. Run browser E2E tests where the environment permits.
6. Verify API contracts.
7. Verify database migrations.
8. Test invalid workflow transitions.
9. Test scheduler failure and recovery.
10. Verify authentication and authorization boundaries.
11. Inspect the resulting Git diff.
12. Fix actionable defects.
13. Ensure documentation matches actual functionality.

Produce a final implementation report containing:

- Architecture implemented
- Completed features
- Frontend/backend integration status
- Actual OpenCode versus mocked functionality
- Tests executed and results
- Known limitations and blockers
- Security considerations
- Exact application startup commands
- Remaining MVP work
- Recommended next priorities

**Execution directive:** Make reasonable implementation decisions independently. Do not stop for minor clarification requests. Escalate only when essential requirements, security-sensitive permissions, or irreversible external actions cannot be safely resolved.

Start with repository discovery, then implement and validate the actual product.

**Your goal is not to produce more architecture documents. Your goal is to deliver the first working version of legos-factory.**