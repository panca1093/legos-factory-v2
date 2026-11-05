<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import {
  ChevronLeft,
  GitBranch,
  MessageSquare,
  Trash2,
  FileText,
} from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import IconButton from "../../components/ui/IconButton.vue";
import { api } from "../../services/api/client";
import { useSession } from "../../stores/session";
import type { KBDocDetail } from "../../types/api";
import { renderMarkdown, tocOf, when } from "./notes";

const props = defineProps<{ docId: string; discussOpen: boolean }>();
const emit = defineEmits<{
  open: [id: string];
  back: [];
  changed: [];
  closed: [];
  remove: [detail: KBDocDetail];
  ask: [quote?: string];
}>();
const session = useSession();

const detail = ref<KBDocDetail | null>(null),
  error = ref(""),
  busy = ref(false),
  editing = ref(false),
  bodyDraft = ref(""),
  fmDraft = ref(""),
  note = ref(""),
  confirming = ref<"" | "discard" | "reset">("");
const root = ref<HTMLElement>(),
  prose = ref<HTMLElement>();
const sel = ref<{ left: number; top: number; text: string } | null>(null);

const idQuery = (id: string) => `id=${encodeURIComponent(id)}`;
const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));
const fmt = (v: unknown) => (typeof v === "string" ? v : JSON.stringify(v));
const shortSha = (sha?: string) => (sha ? sha.slice(0, 10) : "unversioned");

const canEdit = computed(() => !!detail.value?.editable && session.canWrite);
const isDraft = computed(() => detail.value?.doc.status === "draft");
const isRepo = computed(() => detail.value?.doc.source === "repo");
const canDelete = computed(() => {
  const d = detail.value;
  return (
    canEdit.value &&
    !isRepo.value &&
    !isDraft.value &&
    !d!.default_available &&
    (!!d!.doc.project_id || session.isAdmin)
  );
});
const toc = computed(() => (detail.value ? tocOf(detail.value.body) : []));
const html = computed(() =>
  detail.value ? renderMarkdown(detail.value.body) : "",
);
const minutes = computed(() =>
  Math.max(1, Math.round((detail.value?.body.split(/\s+/).length ?? 0) / 200)),
);
const refs = computed(() => {
  const seen = new Set<string>();
  return [
    ...(detail.value?.links ?? []),
    ...(detail.value?.backlinks ?? []),
  ].filter((d) => !seen.has(d.id) && !!seen.add(d.id));
});

async function load(id: string) {
  editing.value = false;
  confirming.value = "";
  note.value = "";
  error.value = "";
  sel.value = null;
  busy.value = true;
  try {
    detail.value = await api.get<KBDocDetail>(`/api/v1/kb/doc?${idQuery(id)}`);
  } catch (e) {
    detail.value = null;
    error.value = errMsg(e);
  } finally {
    busy.value = false;
  }
  if (root.value) root.value.scrollTop = 0;
}
watch(() => props.docId, load, { immediate: true });

function startEdit() {
  if (!detail.value) return;
  bodyDraft.value = detail.value.body;
  fmDraft.value = JSON.stringify(detail.value.frontmatter ?? {}, null, 2);
  error.value = "";
  editing.value = true;
}
function cancelEdit() {
  editing.value = false;
  confirming.value = "";
  error.value = "";
}

async function act(fn: () => Promise<void>) {
  busy.value = true;
  error.value = "";
  try {
    await fn();
  } catch (e) {
    error.value = errMsg(e);
  } finally {
    busy.value = false;
  }
}
const reload = async (id: string) =>
  (detail.value = await api.get<KBDocDetail>(`/api/v1/kb/doc?${idQuery(id)}`));

async function save() {
  if (!detail.value) return;
  let frontmatter: unknown;
  try {
    frontmatter = JSON.parse(fmDraft.value);
  } catch (e) {
    error.value = `Frontmatter is not valid JSON: ${errMsg(e)}`;
    return;
  }
  if (
    typeof frontmatter !== "object" ||
    frontmatter === null ||
    Array.isArray(frontmatter)
  ) {
    error.value = "Frontmatter must be a JSON object.";
    return;
  }
  const id = detail.value.doc.id;
  await act(async () => {
    await api.put(`/api/v1/kb/doc?${idQuery(id)}`, {
      frontmatter,
      body: bodyDraft.value,
    });
    editing.value = false;
    await reload(id);
    emit("changed");
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
    emit("closed");
  });
}
async function discard() {
  if (!detail.value) return;
  const id = detail.value.doc.id;
  await act(async () => {
    await api.post(`/api/v1/kb/doc/discard?${idQuery(id)}`);
    emit("closed");
  });
}
async function reset() {
  if (!detail.value) return;
  const id = detail.value.doc.id;
  await act(async () => {
    await api.post(`/api/v1/kb/doc/reset?${idQuery(id)}`);
    confirming.value = "";
    await reload(id);
    emit("changed");
  });
}

function jump(id: string) {
  root.value
    ?.querySelector(`#${id}`)
    ?.scrollIntoView?.({ behavior: "smooth", block: "start" });
}

function checkSelection() {
  setTimeout(() => {
    const s = window.getSelection();
    const text = s?.toString().trim() ?? "";
    if (
      !s?.rangeCount ||
      text.length < 8 ||
      !prose.value?.contains(s.anchorNode) ||
      !root.value
    ) {
      sel.value = null;
      return;
    }
    const r = s.getRangeAt(0).getBoundingClientRect();
    const box = root.value.getBoundingClientRect();
    sel.value = {
      text,
      left: Math.max(90, r.left - box.left + r.width / 2),
      top: Math.max(64, r.top - box.top + root.value.scrollTop - 44),
    };
  }, 10);
}
async function askSelection() {
  const q = sel.value?.text;
  sel.value = null;
  window.getSelection()?.removeAllRanges();
  emit("ask", q);
  await nextTick();
}
</script>
<template>
  <article ref="root" class="nb-read" :aria-busy="busy" @mousedown="sel = null">
    <div v-if="!detail" class="nb-empty">
      <p v-if="error" class="notice" role="alert">{{ error }}</p>
      <p v-else class="muted" role="status">Loading note…</p>
    </div>
    <template v-else>
      <div class="nb-bar">
        <IconButton class="nb-back" label="Back to notes" @click="emit('back')"
          ><ChevronLeft class="icon"
        /></IconButton>
        <span class="small muted"
          ><span class="nb-pill" :class="detail.doc.status">{{
            detail.doc.status
          }}</span>
          <span class="nb-pill" :class="detail.doc.trust">{{
            detail.doc.trust
          }}</span>
          Updated {{ when(detail.doc.indexed_at).toLowerCase() }} ·
          {{ minutes }} min read</span
        >
        <div class="acts">
          <IconButton
            v-if="canDelete && !editing"
            label="Delete note"
            data-test="kb-delete"
            class="nb-del"
            @click="emit('remove', detail)"
            ><Trash2 class="icon"
          /></IconButton>
          <template v-if="canEdit && !confirming">
            <Button
              v-if="!editing"
              variant="ghost"
              small
              :disabled="busy"
              data-test="kb-edit"
              @click="startEdit"
              >Edit</Button
            >
            <Button
              v-if="isDraft && !editing"
              variant="primary"
              small
              :disabled="busy"
              data-test="kb-approve"
              @click="approve"
              >Approve</Button
            >
            <Button
              v-if="isDraft && !editing"
              variant="danger"
              small
              :disabled="busy"
              data-test="kb-discard"
              @click="confirming = 'discard'"
              >Discard</Button
            >
            <Button
              v-if="detail.default_available && !editing"
              variant="ghost"
              small
              :disabled="busy"
              data-test="kb-reset"
              @click="confirming = 'reset'"
              >Reset to default</Button
            >
          </template>
          <Button
            v-if="!editing"
            :variant="discussOpen ? undefined : 'primary'"
            small
            data-test="kb-ask"
            @click="emit('ask')"
            ><MessageSquare class="icon" />{{
              discussOpen ? "Hide discussion" : "Ask about this"
            }}</Button
          >
        </div>
      </div>

      <div v-if="editing" class="nb-edit">
        <h1 class="nb-title">{{ detail.doc.title }}</h1>
        <p v-if="error" class="notice" role="alert">{{ error }}</p>
        <label for="kb-fm">Frontmatter (JSON)</label>
        <textarea
          id="kb-fm"
          v-model="fmDraft"
          rows="7"
          spellcheck="false"
          class="short"
        />
        <label for="kb-body">Body (markdown)</label>
        <textarea id="kb-body" v-model="bodyDraft" spellcheck="false" />
        <div class="row">
          <Button
            variant="primary"
            small
            :disabled="busy"
            data-test="kb-save"
            @click="save"
            >Save</Button
          >
          <Button small variant="ghost" :disabled="busy" @click="cancelEdit"
            >Cancel</Button
          >
        </div>
      </div>
      <div v-else class="nb-doc">
        <h1>{{ detail.doc.title }}</h1>
        <p class="nb-path small muted">
          <code>{{ detail.doc.path }}</code> · <GitBranch class="icon" /><code
            data-test="kb-sha"
            >{{ shortSha(detail.doc.commit_sha) }}</code
          >
        </p>
        <p v-if="isRepo" class="notice" data-test="repo-readonly">
          Changes to repository documents go through the repository.
        </p>
        <p v-if="error" class="notice" role="alert">{{ error }}</p>
        <div v-if="confirming" class="notice" data-test="kb-confirm">
          {{
            confirming === "discard"
              ? "Discard this draft? The file is deleted."
              : "Reset to the default playbook? Your edits are replaced."
          }}
          <Button
            variant="danger"
            small
            :disabled="busy"
            data-test="kb-confirm-yes"
            @click="confirming === 'discard' ? discard() : reset()"
            >Confirm</Button
          >
          <Button
            small
            variant="ghost"
            :disabled="busy"
            @click="confirming = ''"
            >Cancel</Button
          >
        </div>
        <div v-if="isDraft && canEdit" class="nb-approve">
          <label for="kb-note">Approval note (optional)</label>
          <input id="kb-note" v-model="note" class="lg-field" />
        </div>
        <nav v-if="toc.length > 1" class="toc" aria-label="On this page">
          <span>On this page</span>
          <a
            v-for="t in toc"
            :key="t.id"
            :href="`#${t.id}`"
            @click.prevent="jump(t.id)"
            >{{ t.text }}</a
          >
        </nav>
        <!-- eslint-disable-next-line vue/no-v-html -- escape-then-format renderer -->
        <div
          ref="prose"
          class="prose"
          data-test="kb-rendered"
          @mouseup="checkSelection"
          @keyup="checkSelection"
          v-html="html"
        />
        <details
          v-if="Object.keys(detail.frontmatter ?? {}).length"
          class="props"
        >
          <summary>Properties</summary>
          <dl class="small" data-test="kb-frontmatter">
            <template v-for="(v, k) in detail.frontmatter" :key="k">
              <dt>{{ k }}</dt>
              <dd>
                <code>{{ fmt(v) }}</code>
              </dd>
            </template>
          </dl>
        </details>
        <div v-if="refs.length" class="refs" data-test="kb-links">
          <div class="kind small muted">Linked notes</div>
          <button
            v-for="l in refs"
            :key="l.id"
            type="button"
            class="ref"
            @click="emit('open', l.id)"
          >
            <FileText class="icon" />{{ l.title }}
          </button>
        </div>
      </div>
      <button
        v-if="sel"
        type="button"
        class="sel-ask"
        data-test="kb-ask-selection"
        :style="{ left: `${sel.left}px`, top: `${sel.top}px` }"
        @mousedown.prevent
        @click="askSelection"
      >
        <MessageSquare class="icon" />Ask about selection
      </button>
    </template>
  </article>
</template>
