# Knowledge index — spec (draft v2)

Status: draft for review · 2026-11-05 · branch `feat/claude-acp-host`

## Why

Agents decide without the team's context. Examples: the specialist picker calls a `.ts` business-logic change "frontend", and the Foreman escalates trivia to a human. The repository already holds most of that context. The factory should **follow the repo by default** and must not copy it. Whatever the factory itself knows (its journal, its default playbooks, what it learned from human decisions) lives as files the factory owns.

The idea borrows gbrain's concept without its code:
- markdown files are the system of record;
- the database is only an index that can be rebuilt from the files;
- links form a graph, extracted without an LLM.

## Two sources, one index

| Source | Where | Who writes | What |
|---|---|---|---|
| `repo` | The project repository, at the work order's commit | The repo's own process (people and PRs, or agents when the repo's own docs or skills ask them to update a spec or ADR as part of the change) | CLAUDE.md, docs, ADRs, the wiki, reviewer routing |
| `factory` | The factory home bundle `<data_dir>/knowledge/`, a git repo in OKF format | The factory only | Default playbooks, the work-order journal (replacing `knowledge_records`), learning drafts, confirmed reviewer routing |

**Precedence:** repo and code win over the factory's playbooks. When an agent cannot tell which applies, it asks a human through the existing CLARIFY path. This rule is stated in every pointer block (see Delivery).

## Goals (v1)

1. Index both sources, storing **pointers and linkage only**: path, commit, hash, title, description, links, and a contentless keyword index.
2. **Replace `knowledge_records`.** Every `Catalog.Record` call writes an OKF file into the factory bundle and commits it. The Architect's prior-knowledge lookup and the Knowledge page read through the index. Existing rows are exported to files once.
3. Give each agent run a short, deterministic list of relevant documents from both sources. The agent reads the files itself.
4. **Deterministic reviewer routing:** the repo declares it, or the factory proposes it once and a human confirms. The LLM never picks reviewers.
5. **Default playbooks** for the Foreman (escalation policy) and the Inspector specialists, shipped embedded and seeded into the factory bundle on first start.
6. **Learning drafts:** a disputed finding or an answered clarification lets the Cataloger write a `status: draft` playbook change into the factory bundle. A human approves it (`verified: human:<id>`, `status: stable`) or discards it. Only stable playbooks are offered to agents.
7. **Knowledge UI:** browse both sources. Edit, approve or discard factory documents. Reset a default playbook. Repo documents are read-only here, because they change through the repo.
8. **Hybrid retrieval with a factory-native embedder.** There is no embedding provider: `kb.HashEmbedder` hashes word unigrams and character trigrams into a signed, L2-normalised 512-dim vector. That captures word forms and partial matches ("rounding" vs "round", mixed Indonesian and English); it is not semantic (it cannot see synonyms). The search fuses keyword (bm25) and vector (cosine) rankings with RRF (k=60). The `kb.Embedder` interface lets a semantic model be swapped in later.
9. **Repo changes inside a work order.** When the repo's own docs or skills say a change must update a spec, ADR or doc, the Builder does it in the work order's diff, and it is reviewed with the code. Nothing extra is built for this: the Builder already reads the repo's CLAUDE.md and docs, and the pointer block tells it which docs exist.

## Non-goals (v1)

- A semantic embedding model or an external embedding provider.
- The factory writing to the repo outside a work order's diff.
- Searching across projects.
- A rich editor. v1 uses a markdown text area and a preview.

## Factory bundle layout

```
<data_dir>/knowledge/            # git repo, one commit per write
  index.md, log.md               # OKF reserved files, regenerated on write
  playbooks/
    foreman/escalation.md        # type: Playbook, role: foreman
    inspector/<specialty>.md     # type: Playbook, role: inspector, specialty: backend|testing|…
  projects/<project_id>/
    reviewer-routing.md          # type: Reviewer Routing (only when the repo declares none)
    journal/<wo_id>/<seq>-<kind>.md   # type: Journal Entry, one per former knowledge record
    drafts/<id>.md               # type: Playbook, status: draft, proposed by the Cataloger
```

- **Trust mapping** (from the existing `Trust`):
  - `verified` → `verified: {by: process:legos-factory}`;
  - `human_decision` → `verified: {by: human:<user>}`;
  - `ai_interpretation` → `generated: {by: <role>/<model>}` only, which makes it unverified.
- **Fields that are not OKF** (`work_order`, `kind`, `commit_sha`, `source_refs`) go in frontmatter as extra keys. OKF allows them.
- **Writes are serialized** by one mutex and committed with the factory identity. A run records the bundle SHA it saw.

## Reviewer routing

A document anywhere in either source with `type: Reviewer Routing`:

```yaml
---
type: Reviewer Routing
routes:
  - paths: ["src/**/*.ts", "!src/**/*.test.ts"]
    reviewers: [backend]
  - paths: ["**/*.test.ts"]
    reviewers: [testing]
  - paths: ["src/ui/**", "**/*.vue"]
    reviewers: [frontend, ui_ux]
---
```

- **Resolution:** the repo's routing doc wins. Otherwise the factory's `projects/<id>/reviewer-routing.md` is used. When there is neither, the factory writes a proposal from the current path heuristics as `status: draft` and asks the human to confirm it once, at the first inspection.
- **Matching:** every changed file collects the reviewers of all routes it matches, and `!` excludes a path. `testing` is always included, and the cap stays at 3 specialists. The result is fully deterministic and recorded in the `inspection_specialists` event, together with the matched route.

## Index (SQLite, one new migration)

```sql
CREATE TABLE kb_docs (
  id            TEXT PRIMARY KEY,          -- source:project_id:path
  source        TEXT NOT NULL,             -- repo | factory
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,  -- NULL for factory-wide playbooks
  path          TEXT NOT NULL,
  commit_sha    TEXT NOT NULL,
  content_hash  TEXT NOT NULL,
  type          TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'stable',
  trust         TEXT NOT NULL DEFAULT 'unverified',  -- unverified | machine | human, derived from OKF
  title         TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  meta          TEXT NOT NULL DEFAULT '{}',          -- remaining frontmatter as JSON (role, specialty, routes, work_order…)
  indexed_at    INTEGER NOT NULL
);
CREATE TABLE kb_links (from_doc TEXT NOT NULL REFERENCES kb_docs(id) ON DELETE CASCADE,
                       to_doc   TEXT NOT NULL, PRIMARY KEY (from_doc, to_doc));
CREATE VIRTUAL TABLE kb_fts USING fts5(title, description, headings, body,
  content='', contentless_delete=1);     -- searchable; the text is not stored (verified on SQLite 3.54.1)
CREATE TABLE kb_vectors (doc_id TEXT PRIMARY KEY REFERENCES kb_docs(id) ON DELETE CASCADE,
                         embedder TEXT NOT NULL, vec BLOB NOT NULL);  -- float32 little-endian
```

The FTS rowid maps through a `kb_fts_map(doc_id TEXT PRIMARY KEY, fts_rowid INTEGER)` table, because `kb_docs.id` is text.

- **What is indexed:** `**/*.md`. Skipped: `.git`, `node_modules`, `vendor`, `dist`, `build`, git-ignored paths, files over 256 KB, and anything past 2,000 files per source.
- **Links:** relative markdown links and `[[wiki links]]` (resolved by base name).
- **Rebuild:** dropping the three tables and re-syncing both sources gives the same index.

## Sync

- **`repo`:** once per planning start, after the workspace is prepared, at the work order's base commit; never again during implementation, so nothing a builder writes reaches the index or reviewer routing. Only git-tracked regular files are indexed (no untracked files, no symlinks), and bodies are read back with `git show <commit_sha>:<path>`. Unchanged hashes are skipped. Emits `knowledge_indexed {source, commit_sha, docs, changed, removed}`.
- **`factory`:** synchronously after each write (one file at a time), and in full at startup.

## Retrieval and delivery

Agents never read factory files. The factory resolves what each role needs and puts it in the prompt, so no agent needs read access to `<data_dir>/knowledge`.

- **Playbooks are inlined.** Stable playbooks (type `Playbook`, meta `role` = the agent role, and for inspectors meta `specialty` = the pick) are copied into the prompt, factory-wide and the project's own. The Foreman review prompt gets `playbooks/foreman/*`, an Inspector gets its specialty's playbook, and the Architect and Builder get any playbook whose role matches. Drafts and deprecated docs are never offered.
- **Repository playbooks come first.** A repo doc with `type: Playbook` and a `role` (plus an optional `specialty`; without one it applies to every specialty of that role) is inlined the same way, under its own heading and ahead of the factory playbooks, because the repo wins. It is read with `git show` at the indexed planning-base commit, so a playbook a builder writes mid-work-order never applies. CLAUDE.md is not inlined: it is written for builders, and reviewers see it as a pointer. Playbooks are never listed as pointers.
- **Journal hits stay one-line summaries**, as the Architect's prior-knowledge block does today.
- **Repo docs are pointers.** For the Architect, Foreman review, Builder and Inspector the index is searched (source `repo`, the project) with the query text: the work order, plus the task (Builder) or the plan (Foreman), plus the changed files and specialty (Inspector). The top 8 hits go into the prompt as pointers, never content, and linked docs are marked as see-also:
  ```
  Repository documents that may matter for this work. They are in your workspace; read the ones you need:
  - docs/money.md — Money is integer rupiah — …
  - docs/ledger.md — Ledger — … (see also, linked from docs/money.md)
  If a repository document and a factory playbook disagree, follow the repository. If you cannot tell which applies, ask a human.
  ```
  With no hits there is no repo block; the precedence line stays only when a playbook was inlined.
- **Audit:** `agent_started` carries `{kb_repo_commit, kb_factory_commit, kb_docs:[ids pointed to or inlined]}`.

## Code layout and contracts

- `internal/model/kb.go`: shared types (`KBDoc`, `KBParsed`, `KBQuery`, `KBHit`, `ReviewerRoute`). Already written; treat it as fixed.
- `internal/kb/index.go`: the `Index` and `Embedder` interfaces. Already written; treat it as fixed.
- `internal/kb`:
  - OKF parse: frontmatter, title and description fallbacks, headings, link resolution;
  - `HashEmbedder`;
  - `Bundle`: git, write, read, `index.md`/`log.md`, reset, embedded defaults;
  - `Sync`: walking a root into an `Index`.
- `internal/store/kb.go` and migration `006_kb.sql`: `KBRepo` implements `kb.Index`.
- `internal/usecase/routing.go`: `MatchReviewers` and `ProposeRoutes`, pure.
- `internal/usecase`, wiring:
  - sync hooks;
  - `JournalStore` implementing `KnowledgeStore` on the bundle and index (replaces `store.KnowledgeRepo`);
  - the retriever and prompt blocks;
  - routing in `handleInspect`;
  - learning drafts.
- `internal/http/kb.go`: the API below.

## HTTP API

Doc ids contain `:` and `/`, so they travel in the `id` query parameter.

| Method and path | Body | Result |
|---|---|---|
| `GET /api/v1/kb/docs?project_id=&source=&type=&status=&q=` | — | `{docs: KBDoc[]}`, ranked when `q` is set, else ordered by path; `status` defaults to stable |
| `GET /api/v1/kb/doc?id=` | — | `{doc, frontmatter, body, links: KBDoc[], backlinks: KBDoc[], editable, default_available}` |
| `PUT /api/v1/kb/doc?id=` | `{frontmatter, body}` | `{doc}`; factory docs only, a repo doc gives 409; engineer role |
| `POST /api/v1/kb/doc/approve?id=` | `{note?}` | `{doc}`; draft becomes stable with `verified: human:<user id>` |
| `POST /api/v1/kb/doc/discard?id=` | — | 204; deletes the draft file |
| `POST /api/v1/kb/doc/reset?id=` | — | `{doc}`; restores the embedded default playbook |

- **Access:** project-scoped docs need project access (`Projects.Authorize`); factory-wide docs are visible to every signed-in user.
- **`body`** is read from the file: for a repo doc, from the newest work order workspace of the project (or the project's local repo path) at `commit_sha`; for a factory doc, from the bundle.
- **The existing `/api/v1/knowledge` endpoints** keep working through `JournalStore`.

## Knowledge UI

- **List:** filter by source, type, status, trust and project, with search.
- **Document view:** rendered markdown, frontmatter, links and backlinks.
- **Factory docs:** edit in a markdown text area, then save, which commits. Drafts get Approve or Discard. Default playbooks get Reset to default.
- **Repo docs:** read-only, with the commit they were read at.

## Migration of `knowledge_records`

1. On startup, if the table has rows and the bundle has no journal yet, export each row to its file, commit once, and index it.
2. `Cataloger.Record`, `Relevant` and the knowledge HTTP handlers switch to the bundle and the index.
3. The table is dropped in a later migration, after one release. This keeps a rollback path.

## Verification

- **Store:**
  - sync indexes the docs with title and description fallbacks;
  - unchanged hashes are skipped;
  - deleted files are removed;
  - links resolve;
  - deprecated docs and drafts are never retrieved;
  - FTS input is sanitized;
  - a rebuild gives the same rows;
  - trust is derived from `verified`.
- **Factory bundle:**
  - `Record` writes a valid OKF file (non-empty `type`, parseable frontmatter) and commits it;
  - concurrent writes are serialized;
  - the export reproduces every existing row.
- **Routing:**
  - the repo doc beats the factory doc;
  - `!` exclusions work;
  - with neither, a draft is proposed;
  - the same changed files always give the same reviewers.
- **Usecase:**
  - each role's prompt carries the pointer block and the precedence rule;
  - the Foreman prompt inlines the escalation playbook;
  - `agent_started` records the commits and doc ids;
  - an approved draft appears in the next run, and a discarded one never does.
- **UI:**
  - list and filter both sources;
  - edit and commit a factory doc;
  - approve and discard a draft;
  - repo docs are read-only.
- **Live (legos-pos-testbed):**
  - add `docs/money.md` and a `type: Reviewer Routing` doc to the repo;
  - run a pricing work order;
  - confirm three things: the plan follows the doc without a clarification, the inspection picks `backend` rather than `frontend` and says why, and the run's audit lists both commits.

## Steps

1. Factory bundle: layout, OKF writer, git commit, embedded default playbooks seeded on first start. Verify: bundle tests.
2. Index: migration, sync for both sources, `Retriever` (FTS5 and links). Verify: store tests, including rebuild.
3. Replace `knowledge_records`: `Record`, `Relevant` and the handlers go through the bundle and index, plus a one-time export. Verify: existing cataloger and knowledge tests pass against the new path, plus the export test.
4. Delivery: playbooks inlined per role, repo pointer block with the precedence line, and the audit payload. Verify: prompt tests.
5. Reviewer routing: parse, resolve, draft proposal, deterministic picks. Verify: routing tests.
6. Learning drafts: the Cataloger proposes drafts from disputes and clarifications, with approve and discard. Verify: usecase tests.
7. Knowledge UI: list, view, edit, approve, discard and reset. Verify: unit tests, then a browser check.
8. Live run on legos-pos-testbed. Verify: the three checks above.

## Open points to confirm while building

- Draft generation needs a rule for when the Cataloger proposes, to avoid noise. Proposal: on every dispute, and when the same kind of clarification has been answered twice in one project.
