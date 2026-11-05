<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { BookOpen, GitBranch, Search } from "lucide-vue-next";
import AppDialog from "../../components/ui/AppDialog.vue";
import { api } from "../../services/api/client";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import type { KBDoc, KBDocDetail } from "../../types/api";

const factory = useFactory();
const session = useSession();

const projectId = ref(""),
  source = ref(""),
  type = ref(""),
  status = ref("stable"),
  text = ref("");
const docs = ref<KBDoc[]>([]),
  draftCount = ref(0),
  loading = ref(true),
  error = ref("");

const detail = ref<KBDocDetail | null>(null),
  detailError = ref(""),
  busy = ref(false),
  editing = ref(false),
  bodyDraft = ref(""),
  fmDraft = ref(""),
  note = ref(""),
  confirming = ref<"" | "discard" | "reset">("");

const idQuery = (id: string) => `id=${encodeURIComponent(id)}`;
const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));

const types = computed(() =>
  [...new Set([...docs.value.map((d) => d.type), type.value])].filter(Boolean),
);
const canEdit = computed(() => !!detail.value?.editable && session.canWrite);
const isDraft = computed(() => detail.value?.doc.status === "draft");
const isRepo = computed(() => detail.value?.doc.source === "repo");

function listQuery(extra: Record<string, string>) {
  const q = new URLSearchParams();
  if (projectId.value) q.set("project_id", projectId.value);
  for (const [k, v] of Object.entries(extra)) q.set(k, v);
  return `/api/v1/kb/docs?${q}`;
}

async function refreshDraftCount() {
  try {
    const res = await api.get<{ docs: KBDoc[] }>(
      listQuery({ source: "factory", status: "draft" }),
    );
    draftCount.value = (res.docs ?? []).length;
  } catch {
    draftCount.value = 0;
  }
}

async function search() {
  loading.value = true;
  error.value = "";
  const extra: Record<string, string> = {};
  if (source.value) extra.source = source.value;
  if (type.value.trim()) extra.type = type.value.trim();
  if (status.value) extra.status = status.value;
  if (text.value.trim()) extra.q = text.value.trim();
  try {
    const res = await api.get<{ docs: KBDoc[] }>(listQuery(extra));
    docs.value = res.docs ?? [];
  } catch (e) {
    docs.value = [];
    error.value = errMsg(e);
  } finally {
    loading.value = false;
  }
}

function showDrafts() {
  status.value = "draft";
  source.value = "factory";
  return search();
}

async function refreshAll() {
  await Promise.all([search(), refreshDraftCount()]);
}

onMounted(async () => {
  await Promise.allSettled([factory.refreshProjects()]);
  await refreshAll();
});

function projectName(id?: string) {
  if (!id) return "Factory-wide";
  return factory.projects.find((p) => p.id === id)?.name ?? id;
}

function resetEdit() {
  editing.value = false;
  confirming.value = "";
  note.value = "";
  detailError.value = "";
}

async function open(id: string) {
  resetEdit();
  busy.value = true;
  try {
    detail.value = await api.get<KBDocDetail>(`/api/v1/kb/doc?${idQuery(id)}`);
  } catch (e) {
    detail.value = null;
    error.value = errMsg(e);
  } finally {
    busy.value = false;
  }
}

function close() {
  detail.value = null;
  resetEdit();
}

function startEdit() {
  if (!detail.value) return;
  bodyDraft.value = detail.value.body;
  fmDraft.value = JSON.stringify(detail.value.frontmatter ?? {}, null, 2);
  detailError.value = "";
  editing.value = true;
}

async function act(fn: () => Promise<void>) {
  busy.value = true;
  detailError.value = "";
  try {
    await fn();
  } catch (e) {
    detailError.value = errMsg(e);
  } finally {
    busy.value = false;
  }
}

async function save() {
  if (!detail.value) return;
  let frontmatter: unknown;
  try {
    frontmatter = JSON.parse(fmDraft.value);
  } catch (e) {
    detailError.value = `Frontmatter is not valid JSON: ${errMsg(e)}`;
    return;
  }
  if (
    typeof frontmatter !== "object" ||
    frontmatter === null ||
    Array.isArray(frontmatter)
  ) {
    detailError.value = "Frontmatter must be a JSON object.";
    return;
  }
  const id = detail.value.doc.id;
  await act(async () => {
    await api.put(`/api/v1/kb/doc?${idQuery(id)}`, {
      frontmatter,
      body: bodyDraft.value,
    });
    editing.value = false;
    detail.value = await api.get<KBDocDetail>(`/api/v1/kb/doc?${idQuery(id)}`);
    await refreshAll();
  });
}

async function approve() {
  if (!detail.value) return;
  const id = detail.value.doc.id;
  const n = note.value.trim();
  await act(async () => {
    await api.post(
      `/api/v1/kb/doc/approve?${idQuery(id)}`,
      n ? { note: n } : {},
    );
    close();
    await refreshAll();
  });
}

async function discard() {
  if (!detail.value) return;
  const id = detail.value.doc.id;
  await act(async () => {
    await api.post(`/api/v1/kb/doc/discard?${idQuery(id)}`);
    close();
    await refreshAll();
  });
}

async function reset() {
  if (!detail.value) return;
  const id = detail.value.doc.id;
  await act(async () => {
    await api.post(`/api/v1/kb/doc/reset?${idQuery(id)}`);
    confirming.value = "";
    detail.value = await api.get<KBDocDetail>(`/api/v1/kb/doc?${idQuery(id)}`);
    await refreshAll();
  });
}

type Block = { kind: "h" | "p" | "code"; text: string };
function renderBlocks(body: string): Block[] {
  const out: Block[] = [];
  let para: string[] = [];
  let code: string[] | null = null;
  const flush = () => {
    if (para.length) out.push({ kind: "p", text: para.join("\n") });
    para = [];
  };
  for (const line of body.split("\n")) {
    if (line.trimStart().startsWith("```")) {
      if (code) {
        out.push({ kind: "code", text: code.join("\n") });
        code = null;
      } else {
        flush();
        code = [];
      }
      continue;
    }
    if (code) {
      code.push(line);
      continue;
    }
    const h = /^#{1,6}\s+(.*)$/.exec(line);
    if (h) {
      flush();
      out.push({ kind: "h", text: h[1] });
    } else if (!line.trim()) flush();
    else para.push(line);
  }
  if (code) out.push({ kind: "code", text: code.join("\n") });
  flush();
  return out;
}
const blocks = computed(() =>
  detail.value ? renderBlocks(detail.value.body) : [],
);

const fmt = (v: unknown) => (typeof v === "string" ? v : JSON.stringify(v));
const trustPill = { human: "purple", machine: "orange", unverified: "" };
const statusPill = { stable: "green", draft: "orange", deprecated: "" };
const shortSha = (sha?: string) => (sha ? sha.slice(0, 10) : "unversioned");
</script>
<template>
  <div>
    <section class="section-head">
      <div>
        <span class="pill purple">Knowledge</span>
        <h1>Documents the factory reads.</h1>
        <p class="help">
          Repository documents and factory playbooks, journals and drafts,
          indexed for the agents that plan, build and review.
        </p>
      </div>
      <button
        class="btn"
        type="button"
        data-test="drafts-shortcut"
        @click="showDrafts"
      >
        Drafts to review ({{ draftCount }})
      </button>
    </section>

    <form class="form-grid section-gap" role="search" @submit.prevent="search">
      <div class="form-group">
        <label for="kb-project">Project</label
        ><select id="kb-project" v-model="projectId" @change="refreshAll">
          <option value="">All projects you can access</option>
          <option v-for="p in factory.projects" :key="p.id" :value="p.id">
            {{ p.name }}
          </option>
        </select>
      </div>
      <div class="form-group">
        <label for="kb-source">Source</label
        ><select id="kb-source" v-model="source" @change="search">
          <option value="">All</option>
          <option value="repo">Repository</option>
          <option value="factory">Factory</option>
        </select>
      </div>
      <div class="form-group">
        <label for="kb-type">Type</label
        ><input
          id="kb-type"
          v-model="type"
          list="kb-types"
          placeholder="e.g. Playbook"
          @change="search"
        /><datalist id="kb-types">
          <option v-for="t in types" :key="t" :value="t" />
        </datalist>
      </div>
      <div class="form-group">
        <label for="kb-status">Status</label
        ><select id="kb-status" v-model="status" @change="search">
          <option value="stable">Stable</option>
          <option value="draft">Draft</option>
          <option value="deprecated">Deprecated</option>
          <option value="all">All</option>
        </select>
      </div>
      <div class="form-group">
        <label for="kb-search">Search</label
        ><input
          id="kb-search"
          v-model="text"
          type="search"
          placeholder="e.g. rounding, retries"
        />
      </div>
      <div class="form-group" style="align-self: end">
        <button class="btn primary" type="submit" data-test="kb-search">
          <Search class="icon" />Search
        </button>
      </div>
    </form>

    <p v-if="error" class="notice" role="alert">{{ error }}</p>
    <p v-else-if="loading" class="help" role="status">Loading documents…</p>
    <div v-else-if="!docs.length" class="empty large section-gap">
      <h3>No documents match.</h3>
      <p style="margin-top: 8px">
        Try another status or source, or index a repository with docs.
      </p>
    </div>
    <div v-else class="catalog-grid section-gap">
      <button
        v-for="d in docs"
        :key="d.id"
        class="memory-card"
        data-test="kb-doc"
        @click="open(d.id)"
      >
        <span class="pill" :class="d.source === 'repo' ? '' : 'purple'">{{
          d.source
        }}</span>
        <span class="pill" :class="statusPill[d.status]">{{ d.status }}</span>
        <span class="pill" :class="trustPill[d.trust]">{{ d.trust }}</span>
        <h3 style="margin: 16px 0 8px">{{ d.title }}</h3>
        <p class="small muted" style="line-height: 1.6">{{ d.description }}</p>
        <p class="help" style="margin-top: 16px">
          <code>{{ d.path }}</code> · {{ d.type || "untyped" }} ·
          {{ projectName(d.project_id) }}
        </p>
      </button>
    </div>

    <div class="resource-strip">
      <BookOpen class="icon" /><strong>Sources before certainty.</strong
      ><span
        >Unverified and machine-written documents are labelled; drafts never
        reach an agent until a person approves them.</span
      >
    </div>

    <AppDialog
      :open="!!detail"
      :title="detail?.doc.title ?? 'Document'"
      :subtitle="detail?.doc.path"
      @close="close"
    >
      <template v-if="detail">
        <p>
          <span
            class="pill"
            :class="detail.doc.source === 'repo' ? '' : 'purple'"
            >{{ detail.doc.source }}</span
          >
          <span class="pill" :class="statusPill[detail.doc.status]">{{
            detail.doc.status
          }}</span>
          <span class="pill" :class="trustPill[detail.doc.trust]">{{
            detail.doc.trust
          }}</span>
          <span class="help">
            <GitBranch class="icon" /><code data-test="kb-sha">{{
              shortSha(detail.doc.commit_sha)
            }}</code></span
          >
        </p>
        <p v-if="isRepo" class="notice" data-test="repo-readonly">
          Changes to repository documents go through the repository.
        </p>
        <p v-if="detailError" class="notice" role="alert">{{ detailError }}</p>

        <template v-if="editing">
          <div class="form-group">
            <label for="kb-fm">Frontmatter (JSON)</label
            ><textarea
              id="kb-fm"
              v-model="fmDraft"
              rows="8"
              spellcheck="false"
              style="font-family: monospace"
            />
          </div>
          <div class="form-group">
            <label for="kb-body">Body (markdown)</label
            ><textarea
              id="kb-body"
              v-model="bodyDraft"
              rows="16"
              spellcheck="false"
              style="font-family: monospace"
            />
          </div>
          <div class="row">
            <button
              class="btn primary"
              :disabled="busy"
              data-test="kb-save"
              @click="save"
            >
              Save
            </button>
            <button class="btn" :disabled="busy" @click="resetEdit">
              Cancel
            </button>
          </div>
        </template>
        <template v-else>
          <div class="kb-body" data-test="kb-rendered">
            <template v-for="(b, i) in blocks" :key="i">
              <h3 v-if="b.kind === 'h'">{{ b.text }}</h3>
              <pre v-else-if="b.kind === 'code'">{{ b.text }}</pre>
              <p v-else style="white-space: pre-wrap">{{ b.text }}</p>
            </template>
          </div>
          <dl class="small" data-test="kb-frontmatter">
            <template v-for="(v, k) in detail.frontmatter" :key="k">
              <dt>{{ k }}</dt>
              <dd>
                <code>{{ fmt(v) }}</code>
              </dd>
            </template>
          </dl>
          <template v-if="detail.links?.length">
            <h4>Links</h4>
            <ul data-test="kb-links">
              <li v-for="l in detail.links" :key="l.id">
                <button class="btn link" type="button" @click="open(l.id)">
                  {{ l.title }}
                </button>
              </li>
            </ul>
          </template>
          <template v-if="detail.backlinks?.length">
            <h4>Backlinks</h4>
            <ul data-test="kb-backlinks">
              <li v-for="l in detail.backlinks" :key="l.id">
                <button class="btn link" type="button" @click="open(l.id)">
                  {{ l.title }}
                </button>
              </li>
            </ul>
          </template>

          <div v-if="isDraft && canEdit" class="form-group">
            <label for="kb-note">Approval note (optional)</label
            ><input id="kb-note" v-model="note" />
          </div>
          <p v-if="confirming" class="notice" data-test="kb-confirm">
            {{
              confirming === "discard"
                ? "Discard this draft? The file is deleted."
                : "Reset to the default playbook? Your edits are replaced."
            }}
            <button
              class="btn danger"
              :disabled="busy"
              data-test="kb-confirm-yes"
              @click="confirming === 'discard' ? discard() : reset()"
            >
              Confirm</button
            ><button class="btn" :disabled="busy" @click="confirming = ''">
              Cancel
            </button>
          </p>
          <div v-if="canEdit && !confirming" class="row">
            <button
              class="btn"
              :disabled="busy"
              data-test="kb-edit"
              @click="startEdit"
            >
              Edit
            </button>
            <button
              v-if="isDraft"
              class="btn primary"
              :disabled="busy"
              data-test="kb-approve"
              @click="approve"
            >
              Approve
            </button>
            <button
              v-if="isDraft"
              class="btn danger"
              :disabled="busy"
              data-test="kb-discard"
              @click="confirming = 'discard'"
            >
              Discard
            </button>
            <button
              v-if="detail.default_available"
              class="btn"
              :disabled="busy"
              data-test="kb-reset"
              @click="confirming = 'reset'"
            >
              Reset to default
            </button>
          </div>
        </template>
      </template>
    </AppDialog>
  </div>
</template>
