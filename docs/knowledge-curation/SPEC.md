# Knowledge curation — spec (draft v3)

Status: approved for implementation · 2026-11-06 · scope = the six steps in §9; Phases 2 and 3 (§11) follow

Builds on `docs/repo-knowledge/SPEC.md` (the gbrain-style index: markdown is the system of record, SQLite is a rebuildable index). §9 lists what this spec supersedes there.

## Why

Knowledge should hold documents a person reads and an agent relies on. It fills with the work-order journal instead.

- Before #32 a work order wrote about 12 journal files per revision. `~/.legos-factory/knowledge` held 153 journal entries for 2 projects, listed next to the playbooks. A manual cleanup on 2026-11-06 removed 137 (knowledge repo commit `6788907`).
- #32 reduced the volume (one summary per task, no per-revision verification/implementation entries) but kept the mechanism: every `Catalog.Record` call writes one `kn_*.md` file indexed as a `Journal Entry`.
- `legos-factory-v2` is registered as a project of itself, so its own internals (`requirements/*`, `docs/*`, default playbooks: 26 docs) are indexed as that project's knowledge.
- Result today: 49 notes in the UI, of which 16 are journal and 26 are the factory's own docs.

Target: Knowledge shows only curated documents. The journal still exists as one log per project and is never listed as knowledge.

## Goals

1. Journal = one append-only `log.md` per project. It is not indexed, not listed, not searched.
2. Per project, three document directories: `brief/`, `spec/`, `task/`. Documents can come from a work order, a chat, a handoff, or a person.
3. Work-order documents are records: written once when their stage completes, read-only. Other documents are editable.
4. The Knowledge page is organised per project, not per work order.
5. A project can exclude its own repo paths from Knowledge (§6).
6. The bundle passes the OKF gates of loan-platform's `wiki-lint` (§8).

## Non-goals

- Changing what agents are told to do. Only where prior context comes from changes.
- Moving agent personas out of code (reserved only, D3).
- Designing how chats and handoffs produce documents. The layout and `origin` field make room for them; the writers are a separate spec.
- A semantic embedder. The hash embedder stays.

## 1. What #32 changed and what stays

| #32 behaviour | Under this spec |
|---|---|
| No per-revision verification/implementation records | Stays. |
| `completeWO` builds one deterministic five-section summary, budget 1800 bytes (`taskSummary`) | Stays. The text becomes the **Task Summary** document (§3). |
| Terminal `work_order_completed` record linking every record via `knowledge:<id>` refs | Replaced by the document write. No record ids exist any more, so there are no refs. |
| `taskSummary` "Key decisions" reads `Knowledge.Search(WOID)` | Reads `wof_events` instead (§2). |
| `tasksummary_test.go`, `tests/integration/cataloger_test.go` | Updated. Section content assertions stay; the "one new entry" assertion becomes "one `task/…-summary.md` document". |

## 2. Journal: log only

`Cataloger.Record` keeps its signature and writes one line to `projects/<pid>/log.md`. It no longer writes a record, a file, or an index entry.

```
# Log

## 2026-11-06

* 15:32 `wo_c6680cdd-92d` **pr_decision** (human) — PR approved: "<title>"
* 15:30 `wo_c6680cdd-92d` **findings** (inspector) — backend inspection of revision 11: 6 finding(s), 0 blocker(s)
```

- Format is OKF `log.md`: bare ISO date headings, newest day first. One line per record: time, work-order id, kind, actor, title, then the body clipped to 300 characters.
- The log is not the system of record for details. Findings, events, runs and checks stay in their own tables (`findings`, `wof_events`, `runs`, `quality_checks`). The log is a readable trail.
- `Bundle.Write` appends a line to the root `log.md` on every call (`logEntry`). A project-log append must not go through it, or each record would add a second line to the root log. Add `Bundle.Append(rel, line, message)` that writes the project log and commits without `logEntry`.
- `log.md` is already skipped by the indexer (`kb/factory.go:59`, `kb/sync.go:130`), so it never reaches the list, search or vectors.

**What this removes.** Option "log only" removes addressable records, so these go:

| Today | Change |
|---|---|
| `JournalStore.Get/Search`, the `KnowledgeStore` port, `Cataloger.Find/Inspect` | Removed. |
| `GET /knowledge`, `GET /knowledge/:id` | Removed. The frontend never calls them (not referenced in `frontend/src`). |
| `taskSummary` decisions | From `wof_events`: `clarification_answered`, `plan_revision_requested`, `revision_requested`, `pr_approved`, `pr_declined`, `finding_<status>`. Plan approval is recorded in `task/…-dag.md` frontmatter (`approved_by`). |
| `catalog_summary` job and `handleCatalogSummary` (AI summary reading records by WO) | Retired (D9). The deterministic Task Summary replaces it. |
| `knowledge_records` table | Kept but unused (legacy). The existing `Export` marker is not touched. |

Not verified: that every former decision kind (`architect_review`, `plan_review`, `readiness`) has an event with the same information. Step 1 of §9 adds a test that lists each kind used by `taskSummary` and the event that now feeds it.

## 3. Layout

```
knowledge/
  index.md                      OKF root index, frontmatter: only okf_version
  playbooks/                    factory-wide (as today)
  personas/                     reserved (D3)
  projects/<pid>/
    log.md                      journal (hidden)
    reviewer-routing.md         unchanged, keeps draft/approve
    brief/                      what was asked and why
    spec/                       what must be true when done
    task/                       how it is split, how it ended
    notes/                      free-form project notes (D11)
```

Filenames: `<date>-<slug>-<code>[-<kind>].md`

- `date`: creation date in Asia/Jakarta, `YYYY-MM-DD`.
- `slug`: the first three words of the title, lowercase ASCII (letters and digits only, other characters act as separators), each word cut at 16 characters. An empty result is `untitled`.
- `code`: the last 6 characters of the origin id with hyphens removed, so the file maps back to its work order or chat.
- `kind`: `dag` or `summary`, only for `task/` documents.

```
brief/2026-11-06-ganti-jurnal-verifikasi-c6680c.md
spec/2026-11-06-ganti-jurnal-verifikasi-c6680c.md
task/2026-11-06-ganti-jurnal-verifikasi-c6680c-dag.md
task/2026-11-06-ganti-jurnal-verifikasi-c6680c-summary.md
notes/2026-11-06-<slug>-<code>.md      (code = 6 random hex)
```

`origin_id` in frontmatter keeps the real id (`wo_c6680cdd-92d`, `chat_…`, a handoff id); the file name is only for people. The name is computed once, when the first document of an origin is written; later documents of the same origin reuse its `date-slug-code` prefix, found by `origin_id` in the index, so a changed title never renames a file. No directory per work order; origin is metadata.

### Frontmatter (all project docs)

`type`, `title`, `description` (one sentence), `origin` (`work_order` | `chat` | `handoff` | `manual`), `origin_id`, `project`, `created_at`, `commit_sha` when known. No `status`. A missing status is read as `stable` (`kb/parse.go:84`), so storage is unchanged.

`type` is one of `Brief`, `Spec`, `Task DAG`, `Task Summary`, `Note`. The directory mirrors the type, but the UI and the retrieval filter on `type`.

### Work-order documents

| Doc | Written when | Source |
|---|---|---|
| `brief/<wo>.md` | Intake finished | `wo.Brief`, plus clarification Q&A if any |
| `spec/<wo>.md` | Plan approved | `wo.Spec`, acceptance criteria |
| `task/<wo>-dag.md` | Plan approved | Plan tasks: title, objective, role, deps, scope |
| `task/<wo>-summary.md` | Work order reaches a terminal state, including CANCELLED | `taskSummary` output |

- They are records: no draft/stable, no approval. A document is written when its stage completes and stays as the record of that stage.
- If the plan goes back for revision, `spec` and `dag` are rewritten at the next approval and show the approved result. Earlier versions are in the bundle's git history and the log has the revision request.
- **Read-only:** `PUT /kb/doc` is refused (HTTP 409) for `origin: work_order`. Documents with any other origin, and notes, remain editable by engineers through the existing flow (D4).

### Shape (readable by people and parseable by agents)

Every document opens with one plain sentence saying what it is, then links its siblings with relative links (`[Spec](../spec/wo_x.md)`). Fixed `##` headings per type, in a fixed order:

- **Brief:** Request, Context, Constraints, Questions and answers.
- **Spec:** Objective, Acceptance criteria, Out of scope, Approach.
- **Task DAG:**
  - `## Graph`: one line per task in dependency form, for example `1`, `2 ← 1`, `3 ← 2`.
  - `## Tasks`: one `### N. <title>` section per task with a short paragraph `Role: builder · Depends on: 1 · Scope: <paths or "not set">` and the objective. `N` is the task's position (`task-N`).
- **Task Summary:** Goal, Final verification, Open items and follow-up, Key decisions and rationale, Main changes. The five sections of #32; only the labels change from `Goal:` lines to `##` headings. The 1800-byte budget stays.

The shape is constrained by the current renderer (§7): headings up to `###`, flat lists, paragraphs, code. No tables, no nested lists, no diagram.

## 4. Retrieval and the vector index

"gbrain" in this repo is the concept in `docs/repo-knowledge/SPEC.md` (no code named gbrain exists). How the index treats the new layout, as implemented:

- **One vector per document**, 512-dim hashed word unigrams and character trigrams over `title + description + headings + body`, first 20,000 characters. Lexical, not semantic.
- **Hybrid search**: bm25 plus cosine fused with RRF (k=60). The index stores metadata, links, a contentless keyword index and the vector; document text stays in the markdown files and the index can be rebuilt.
- **Scope**: every query filters by project, source and type. The directory name is metadata only; `type` is what to filter on.
- **Link graph**: relative markdown links resolve against the document's directory (`resolveLink(dir, …)`) and become edges. A search hit also returns up to 4 linked documents as "see also" (`kbSeeAlsoCap`). Cross-linking a work order's brief, spec, DAG and summary means a hit on the summary brings the spec and brief with it.
- **`log.md` leaves the index.** Journal entries stop being retrievable; this is intended.

Consequences for the design:

1. **Fixed headings are shared tokens.** The hash vector has no IDF, so identical headings add the same signal to every summary and flatten differences slightly. `title` and `description` must carry the distinguishing words (the work-order title goes in both).
2. **Long notes are diluted** (one vector per document, no chunking). Acceptable for v1; keep notes focused.
3. **Rewriting a document** is handled: sync compares content hashes and re-embeds only changed files.
4. **`Relevant()` (Architect prior context)** searches `Task Summary`, `Spec`, `Brief` and `Note` documents of the same project, excludes the current `origin_id`, keeps `retrievalLimit` 5 and the `knowledge_retrieved` event (refs become document ids). `Task DAG` is excluded because it adds structure, not outcome. This is a behaviour change: the Architect now sees outcomes and decisions instead of raw journal records. It needs a live check.
5. The precedence rule from the repo-knowledge spec holds: repo and code win over factory documents.

## 5. Knowledge page

The page already groups by project (`notebooksOf`: All notes, Factory-wide, one notebook per project from the project list, shown even with zero documents). There is no status filter and no Drafts notebook: the page loads `status=all`. The change is inside a project:

- No grouping by work order.
- Collapsible folders, in order: Brief, Spec, Task (`Task DAG` and `Task Summary`), Notes, Repository (collapsed by default), Needs review (`status=draft`, any type). Empty folders are shown muted and do not expand. A document sits in exactly one folder: drafts first, then by type, then repo documents; leftovers go to "Other". While searching, the list is flat. Factory-wide has Playbooks and Needs review.
- Each card shows the title, the type, and its origin ("Work order", "Chat", …). Sorted newest first.
- The list and search endpoints return only allow-listed types: `Brief`, `Spec`, `Task DAG`, `Task Summary`, `Note`, `Playbook`, `Reviewer Routing`, plus repo documents that pass §6.
- Work-order documents open read-only (no Edit). Other documents show Edit as today.
- `Reviewer Routing` and learning drafts appear under Needs review and keep Approve/Discard in the reader. Deprecated documents are listed dimmed with a badge.

## 6. Which repo documents count

Today every `.md` in the checkout is indexed as `source=repo`. Add per-project `kb.include` / `kb.exclude` globs (default: include all, exclude nothing, so other projects do not change). For `legos-factory-v2`: exclude `requirements/**`, `docs/**`, `backend/internal/kb/defaults/**`, `README.md`.

D2: an excluded path leaves the index (agents stop seeing it as a pointer) or is only hidden from the list. Hide-only is safer for this project because its Builders need those docs.

## 7. What the UI cannot render today (verified)

The Knowledge reader (`frontend/src/features/knowledge/notes.ts`) is a small escape-then-format renderer:

| Feature | Supported |
|---|---|
| Headings | `#` to `###` only |
| Lists | Flat only; nested items flatten |
| Tables | No (rendered as paragraphs with pipes) |
| Links | `https://` only. A relative link such as `../spec/x.md` is shown as plain text |
| Mermaid | No; no mermaid dependency in `frontend/package.json` |
| Code blocks, quotes, bold, italic, inline code | Yes |

So the DAG is a text graph plus `###` sections, not a table or a picture. Relative links are the right form for the index and for lint (`[G]`), but they are dead text in the reader until it resolves them to document ids (D10). Until then the first line of each document names its siblings by title as well.

## 8. Lint

Source: `loan-platform/.agents/skills/wiki-lint` and `scripts/okf_conformance.sh` (available locally; the Bitbucket MCP returns 401).

Baseline (2026-11-06) on `~/.legos-factory/knowledge`: 35 files, 1 violation: `[B]` root `index.md` must have frontmatter of only `okf_version`.

- Fix `[B]` in the index writer (`regenIndex` in `kb/bundle.go`, root case).
- Add `backend/internal/kb/lint.go` (one file): `[A]` type non-empty, `[B]`, `[C]` sub-index has no frontmatter, `[D]` log date headings, `[G]` relative links resolve. Test against a fixture bundle. `[E]`/`[F]` are cheap and can be added.
- Allowed `type` values live next to the lint: loan-platform's plus the factory's (`Playbook`, `Reviewer Routing`, `Brief`, `Spec`, `Task DAG`, `Task Summary`, `Note`).
- Taxonomy gates `[T1]`–`[T6]` are loan-platform specific and not ported.
- D6: a test only, or also `GET /kb/lint`.

## 9. Migration, tests, supersedes

Order (each step shippable):

1. `Record` appends to the project log. Add `Bundle.Append`. `taskSummary` decisions read `wof_events`; add the event-coverage test from §2. Remove `JournalStore`, `KnowledgeStore`, `Find/Inspect`, `/knowledge` routes and the `catalog_summary` job.
2. Allow-list types in list/search; stop indexing `Journal Entry`. Delete the existing `projects/*/journal/` directories (D8: not migrated; history is in git).
3. Write `brief`, `spec`, `task-dag`, `task-summary` documents at their stages. Tests: each is written at its stage; a cancelled work order still gets a summary; a plan revision rewrites spec and dag; PUT on an `origin: work_order` document is refused.
4. `Relevant()` reads the documents (§4.4). Test: excludes the current origin and other projects.
5. `kb.include/exclude` and the UI folders and read-only state.
6. Lint and the `[B]` fix.

Supersedes in `docs/repo-knowledge/SPEC.md`: goal 2 (journal as OKF files) and the `projects/<pid>/journal/<wo>/<seq>-<kind>.md` layout. Unchanged there: the two sources, precedence, hybrid retrieval, reviewer routing, default playbooks, learning drafts (`drafts/`).

Whole-change check, with the factory off and a fixture project: run one work order. The Knowledge list shows only allow-listed types. `log.md` has one line per record. The project has `brief/`, `spec/`, `task/` documents for that work order, all read-only. A second work order's Architect run receives the first summary. The lint run is clean.

## 10. Decisions

| # | Question | Status / recommendation |
|---|---|---|
| D1 | Journal storage | Closed: `log.md` only. |
| D2 | Excluded repo paths: out of the index or hidden from the list? | Hide only. |
| D3 | Agent personas as documents now or later? | Later; `personas/` reserved. |
| D4 | Read-only rule | Closed: `origin: work_order` is read-only; other origins editable. |
| D5 | Commit per log append or batch? | Per append first. |
| D6 | Lint as a test only or also an endpoint? | Test first. |
| D7 | One DAG and one summary per work order | Closed. |
| D8 | The 16 remaining journal files | Dropped; recoverable from git. |
| D9 | The AI `catalog_summary` job | Closed: the 150-word AI summary is retired; the Cataloger agent role is kept and becomes the Librarian (§11, Phase 3). |
| D10 | Resolve relative `.md` links in the reader | Closed: Phase 2 (§11). |
| D11 | `notes/` directory | Closed: kept. |
| D12 | Filenames | Closed: `<date>-<slug>-<code>[-<kind>].md`, slug is three words (§3). |
| D13 | Reader implementation: extend `notes.ts` or `markdown-it` + `DOMPurify` | Phase 2: `markdown-it` + `DOMPurify`, Mermaid lazy-loaded. |

## 11. Later phases (not in the six steps)

**Phase 2 — Reader.** Scope: `frontend/src/features/knowledge` only.
1. Replace the renderer with `markdown-it` plus `DOMPurify`. Support GFM tables, nested lists and headings up to `######`.
2. Resolve relative `.md` links to documents of the same project by path (the list payload already carries `path`); render unresolved ones as plain text.
3. Render ` ```mermaid ` blocks with a lazy `import("mermaid")`, `securityLevel: "strict"`.
4. The DAG generator adds a table and a `flowchart` block above the text graph. The text graph stays, so the file reads in git and editors. Existing records keep their old shape.
5. vitest cases for tables, nesting, link resolution and XSS payloads. Not verified: bundle-size impact and whether the app's CSP allows Mermaid.

**Phase 3 — Librarian.** The Cataloger agent role is kept and renamed in prompts to Librarian (the role id `cataloger` and its model setting stay). It follows `wiki-maintainer`'s four workflows from loan-platform; the persona's rule that the wiki beats the code is not adopted — repo and code still win (see `docs/repo-knowledge/SPEC.md`).

| Workflow | In the factory | Nature |
|---|---|---|
| compile | Write brief, spec, DAG, summary; regenerate `index.md`; append `log.md` | Deterministic Go (Steps 1–3). |
| lint | OKF gates (§8) | Deterministic (Step 6). |
| ingest | Chat, handoff or raw notes become a draft `brief/` or `notes/` document, labelled `generated` | Agent, through the existing draft/approve flow. |
| audit | After a work order, or on demand: referenced files still exist at `commit_sha`, documents made stale by later changes, orphans, duplicate notes. Findings go to `log.md`. Never rewrites a work-order record | Deterministic checks first; the agent only judges. Every claim carries a file reference. |
| sync | Repo index | Exists (`syncRepo`). |

Replaces the `catalog_summary` job and the `CatalogerSummaries` setting.
