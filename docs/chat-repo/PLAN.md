# Chat reads and analyses the project repo — plan

Goal: a chat bound to a project can open and search that project's code, read-only, and
answer analysis questions about it (explain a flow, find where something is decided,
"what breaks if I change X", review a design). A chat without a project stays as it is.

## Today

- The chat role has no tools at all: `claudeChatMeta` sets `tools: []`, `acpChatDeny` denies
  read/grep/glob on OpenCode-ACP, and `answerPermission` rejects every request for the chat role
  (`backend/internal/agent/acp.go`).
- The chat agent runs in an empty folder (`Chat.Dir`), even when the chat is bound to a project.
- Chat only runs on the ACP harness. Its default model is the Architect's model.
- Only `user_message`, `delta`, `title`, `done`, `error` reach the browser; tool steps do not.
- Changing a chat's project does not drop its kept agent session.

## Decisions (need your yes; all reversible)

| # | Decision | Why | Rejected |
|---|---|---|---|
| 1 | The chat reads a **factory checkout of the project**, made the same way a work order workspace is made (`sandbox.Prepare`), one per project, refreshed when a chat session starts | Works for local and remote projects alike. Holds tracked files only, so an untracked `.env` in your working folder is never in reach. The commit it answers from can be shown | Reading your local folder as is: simplest and sees uncommitted work, but puts untracked secrets in reach and does nothing for remote projects. This reverses my earlier suggestion |
| 2 | Tools are `Read`, `Grep`, `Glob` only | The read-only profile already exists for the Architect and Foreman (`claudeReadOnlyMeta`, plan mode) | `git log` / `blame`, running tests or linters: needs restricted execution, a much larger change. Later |
| 3 | A repo chat loads **no** setting sources, like the other read-only roles | With tools on, your `~/.claude` hooks and plugins would run inside the chat agent and add about 10K tokens per session | Keeping `["user"]` as plain chat does today |
| 4 | A repo chat with model "auto" uses the **Builder's** model, not the Architect's | One question can cost 50–250K input tokens; the Architect's model is the expensive one | Leaving it on the Architect's model |
| 5 | The chat shows what the agent is doing ("Reading chat.go") while it works | A repo turn takes tens of seconds; a silent spinner looks hung | Text only |

## Spike results (2026-11-11, claude-agent-acp, Sonnet, read-only profile + plan mode)

Run against a fresh clone of this repo with a planted fake `.env` and `certs/server.pem`.

| Question | Result |
|---|---|
| Does the read-only profile work for chat-style questions? | Yes. Text streams (255–354 chunks per turn), tool steps arrive, the kept session continues across turns, answers cite `path:line` and the ones checked were right. No plan-mode remarks in the answers |
| Can secret paths be denied? | Yes. Without a rule the agent read both planted secrets. With `Read(./.env)`, `Read(./.env.*)`, `Read(./**/.env)`, `Read(./**/*.pem)`, `Read(./**/*.key)` in `disallowedTools`, both reads were refused and `Grep` no longer returned the `.pem` match |
| Does `sandbox.Prepare` refresh? | No. It is a plain `git clone` into `workspaces/<id>` and fails when the folder exists. A remote project needs its git connection |
| Cost of three real questions | Turn 1: 109K in, 3.6K out, $0.12, 33 s. Turn 2 (follow-up): 113K in, $0.08, 27 s. Turn 3 (new topic): 257K in, $0.13, 34 s. Almost all input is cache reads and writes |

What this changes:

- **Secrets:** use both the tracked-files checkout and the deny rules. The deny rules live in the chat-with-repo meta.
- **Checkout:** `Chat.Repo` clones once (`sandbox.Prepare`, id `chat-<project id>`), then on each new session fetches and resets to the default branch. This refresh is new code.
- **Budget:** reserve $0.25 per repo turn. No spending note is needed at these numbers.
- **Steps:** `Read` steps arrive with an empty target, so "Reading chat.go" needs the file path taken from the tool call. Part of step 2.
- **Reply text:** narration written between tool calls ("Now `compose`, where…") is glued to the start of the answer. Either separate it or accept it; decide in step 3.
- **Long chats:** input per turn grows with the session (257K by turn 3). Not a problem at three turns; watch it in the live check.

Not measured: the Architect's model. At list prices it would be roughly five times these figures — an estimate, not a measurement.

## Not in this change

- Seeing uncommitted work, choosing a branch, or chatting about a work order's own workspace.
- Git history, running commands, web access, MCP servers (`strictMcpConfig` stays on).
- Saving an analysis as a knowledge document.
- The OpenCode HTTP harness (chat does not run there today).

## Design

### Harness (`backend/internal/agent/acp.go`)

- `model.AgentRequest` gets one field, `RepoAccess bool`. It is set only by chat.
- Chat with `RepoAccess`: Claude uses `claudeReadOnlyMeta` and plan mode; OpenCode-ACP skips
  `acpChatDeny`; `answerPermission` allows `read`, `search` and `think` inside the workspace.
- Chat without it: unchanged, no tools.
- Secrets: a tracked-files checkout removes the common case. If the spike shows a reliable deny
  rule for paths, also deny `.env*`, `*.pem`, `*.key`, `id_rsa*`.

### Chat (`backend/internal/usecase/chat.go`)

- New port on `Chat`: `Repo(ctx, projectID) (dir, sha string, err error)` — returns the project's
  checkout, creating or refreshing it. Refresh happens only when a new agent session starts, so
  files never shift in the middle of a conversation.
- `answer`: when the chat has a project and `Repo` succeeds, run in that directory with
  `RepoAccess`, and use a repo preamble: you may read this repository, cite `path:line`, you cannot
  run commands or change files, say so when you did not read the code you are describing.
- First prompt of a session also carries `repoFacts(dir)` and the project's Code Map lines, so the
  agent does not start cold.
- `Update`: changing the project drops the kept session.
- `OnStep` → new `ChatEvent{Kind: "step"}` with the tool name and target.
- Budget: a repo turn reserves more than the current $0.05 estimate; the amount comes from the spike.
- If the checkout fails (no access, repo gone), the turn runs as plain chat and the reply says the
  repository could not be read.

### HTTP and browser

- `http/chat.go` forwards `step` as an SSE event: `{"tools":[{"name","target","failed"}]}`.
- The chat view shows the latest step under the streaming reply and a chip
  "Reading <project> @ <short sha>" on repo chats.

## Steps

1. **Spike** (no merge) → verify: a short note with measured answers to:
   - does a chat-role session with `Read/Grep/Glob` in plan mode stream text and steps as expected on claude-agent-acp;
   - can paths be denied reliably (and does `Grep` honour it);
   - does `sandbox.Prepare` refresh an existing checkout or need a pull step;
   - tokens and cost of three real questions on this repo, first turn and follow-up.
2. **Harness** → verify: agent tests — chat with `RepoAccess` gets the read-only meta and its read
   requests are allowed inside the workspace and rejected outside; chat without it still gets no tools.
3. **Chat use case** → verify: tests — a project chat runs in the checkout with `RepoAccess`; a
   plain chat is unchanged; project change drops the session; a failed checkout falls back; `step`
   events are emitted; the session's first prompt carries facts, later prompts do not.
4. **HTTP + browser** → verify: handler test for the `step` event; in the running app, ask a
   question on a project chat and see steps, the commit chip and an answer that cites `path:line`.
5. **Live check** → verify: the three spike questions asked through the app; record tokens (fresh /
   cache) and cost per turn in the PR.

## Split

| Agent | Steps | Files |
|---|---|---|
| A | 2, 3 | `agent/acp.go`, `model/agent.go`, `usecase/chat.go`, `app/app.go`, their tests |
| B | 4 | `http/chat.go`, `frontend/src/features/chats/*` |

B starts from the event contract above and does not wait for A. Step 1 runs first and may change decisions 1–4.

## Uncertain

- Whether one shared checkout per project is safe while two chats read it and one triggers a refresh. If not: refresh only when no chat session on that project is live.
- Whether the path deny rules also hold on OpenCode-ACP. The spike covered claude-agent-acp only.
- Whether `Glob` lists denied files by name. The spike checked `Read` and `Grep`.
