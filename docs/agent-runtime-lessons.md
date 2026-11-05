# Agent runtime lessons from legos-factory

What building and running legos-factory (Oct 2026) taught us, written for a fresh
agent runtime. Each lesson names the evidence. Related: `opencode-in-container-design.md`.

## 1. One working copy per task

Parallel builders shared one checkout of the repo. Observed in one run:
- two builders rewrote the same `api/internal/model/tenant.go` (a `write` replaces the whole file, so the last writer wins; a half-merged file with a duplicated declaration appeared briefly);
- both chose the next migration number, producing two `000012_*` migrations;
- one builder ran `git checkout HEAD -- …` and wiped the other's uncommitted edits (changed files fell from 24 to 16).

Rules for the new runtime:
- a git worktree (or copy) per task, merged afterwards;
- plans declare which files or areas each task owns, and tasks that overlap are never parallel;
- shared resources with sequence numbers (migrations) are allocated by the runtime, not picked by agents.

## 2. Provider errors are failures, and they come in two kinds

Early on, a provider error (`finish=error`) was read as an empty reply and the task was marked implemented.
- classify errors: **permanent** (usage cap, insufficient funds, privacy or training setting, invalid request) fail once with the provider's message; **transient** (5xx, timeouts) retry with backoff;
- "Insufficient account funds" was first retried three times because it wasn't in the permanent list. Match on type and status, not only message text;
- a builder step that changes no files is a failure (`task_no_changes`), not a success.

## 3. Deny tools by default, per role

Nothing was sandboxed by default in OpenCode 2.1.3: agents read other workspaces freely.
- deny `external_directory` for every role, and everything that writes or executes for read-only roles: `edit`, `write`, `patch`, `apply_patch`, `bash`, `shell`, `execute`;
- denying only the edit tools is useless: the model wrote files through `shell`. And `external_directory: deny` does not stop `shell` from reading other workspaces, so the container is the real boundary;
- use deny, not ask: when an agent asks permission headlessly and the client rejects it, the whole turn is cancelled.

## 4. Make the model, plan and cost explicit

- the same model name existed under two providers: `opencode/glm-5.3-flash` (pay per token, $0 credit, so `402`) and `opencode-go/glm-5.3-flash` (subscription). The picker showed no difference;
- "Workspace default" silently meant the first model listed at startup, a free one, so planning and review ran on a weak model with nothing in the UI saying so;
- show provider, billing type (subscription, free, pay per token) and the resolved model for every role;
- set the model when the session is created. OpenCode accepts a model in the prompt and ignores it;
- agents may fire auxiliary calls on another model (a GPT-6 Luna title request appeared next to builder runs). Pin the small or title model to the run's model.

## 5. Push events, don't poll

- polling a message list broke on ordering (the API lists newest first) and on multi-step replies;
- ACP (stdio JSON-RPC) pushes `tool_call` and `tool_call_update` events, the stop reason and token usage. A 250-line Go client with no SDK worked;
- test the parser against captured real streams: my fake agent missed that a tool update's `content` is an array while a message chunk's is an object, so every tool update was silently dropped;
- report each step, group a run's steps for the UI, and flag a stall after about 3 minutes without a new step.

## 6. Authentication is a product decision

- subscription logins are personal, interactive and tied to terms of use; they suit single-owner self-hosting only. `opencode auth login <integration> --method device` worked in a container with no browser;
- a multi-user or shared runtime needs API keys per workspace, encrypted and injected per run;
- Anthropic's Agent SDK documentation says third parties may not offer claude.ai login or subscription limits in their products without approval. Use API keys for Claude.

## 7. Agents explore a lot before they write

- a builder took about 75 read-only steps before its first real edit, and an `npm` command changed `web/package-lock.json` along the way;
- give builders tighter context (the files that matter, conventions), a step budget, and a check for stray lockfile or generated-file changes before a PR.

## 8. Failed work needs a way back

- "Resume" only cleared the pause; it did not re-run failed jobs. Re-queueing needed direct database edits;
- provide an explicit retry for a failed task, and keep the failure message (model, error type) visible next to it;
- cancellation is final: keep enough state to start a follow-up order from a cancelled one.

## Protocol notes (ACP v1, OpenCode 2.1.3)

- `initialize` → `session/new {cwd}` → `session/set_config_option` (config option with category `model`) → `session/prompt`;
- the prompt result carries `stopReason` and token usage; cost arrives as `usage_update`;
- one agent subprocess per run, per-role config through `OPENCODE_CONFIG_CONTENT`;
- v2 of the spec is a draft; target v1.
