<script lang="ts">
import { ref } from "vue";
import { useToast } from "../../composables/useToast";
import { api } from "../../services/api/client";

const { toast } = useToast();
const idQuery = (id: string) => `id=${encodeURIComponent(id)}`;
const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));

// Module scope: a pending delete must outlive the view that scheduled it.
const pendingIds = ref(new Set<string>());
const queued = new Map<string, { committed: boolean }>();
let reloadList: (() => Promise<void>) | null = null;

if (typeof window !== "undefined")
  window.addEventListener("pagehide", () => {
    for (const [id, e] of queued) {
      if (e.committed) continue;
      e.committed = true;
      fetch(`/api/v1/kb/doc?${idQuery(id)}`, {
        method: "DELETE",
        keepalive: true,
        credentials: "same-origin",
      }).catch(() => {});
    }
  });

function scheduleDelete(id: string, title: string, onUndo: () => void) {
  const entry = { committed: false };
  queued.set(id, entry);
  pendingIds.value = new Set(pendingIds.value).add(id);
  const settle = () => {
    queued.delete(id);
    const s = new Set(pendingIds.value);
    s.delete(id);
    pendingIds.value = s;
  };
  toast(
    `Deleted “${title}”`,
    () => {
      settle();
      onUndo();
    },
    async () => {
      if (entry.committed) return settle();
      entry.committed = true;
      try {
        await api.del(`/api/v1/kb/doc?${idQuery(id)}`);
        await reloadList?.();
        settle();
      } catch (e) {
        settle();
        toast(`Could not delete “${title}”: ${errMsg(e)}`);
        await reloadList?.();
      }
    },
  );
}
</script>
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { BookOpen, ChevronRight, Layers } from "lucide-vue-next";
import SearchField from "../../components/ui/SearchField.vue";
import { useFactory } from "../../stores/factory";
import type { KBDoc, KBDocDetail } from "../../types/api";
import DiscussPanel from "./DiscussPanel.vue";
import NoteReader from "./NoteReader.vue";
import {
  highlight,
  groupFolders,
  inNotebook,
  isProjectNotebook,
  originLabel,
  NB_ALL,
  notebooksOf,
  snippet,
  ts,
  when,
} from "./notes";
import "./knowledge.css";

const factory = useFactory();
const router = useRouter();
const route = useRoute();

const docs = ref<KBDoc[]>([]),
  loading = ref(true),
  error = ref("");
const q = ref(""),
  nb = ref(NB_ALL),
  folderOpen = ref<Record<string, boolean>>({}),
  openId = ref(""),
  openTitle = ref(""),
  discuss = ref(false),
  quote = ref(""),
  mobile = ref<"list" | "read" | "talk">("list"),
  editState = ref<boolean | null>(null);
let pinned = "",
  picked = false;

const projectName = (id?: string) =>
  !id
    ? "Factory-wide"
    : (factory.projects.find((p) => p.id === id)?.name ?? id);

const visible = computed(() =>
  docs.value.filter((d) => !pendingIds.value.has(d.id)),
);
const notebooks = computed(() => notebooksOf(visible.value, factory.projects));
const sections = computed(() => {
  const l = visible.value.filter((d) => inNotebook(d, nb.value));
  if (q.value.trim() || nb.value === NB_ALL)
    return [
      {
        key: "",
        label: "",
        docs: q.value.trim() ? l : [...l].sort((a, b) => ts(b) - ts(a)),
        folder: false,
        open: true,
      },
    ];
  return groupFolders(l, isProjectNotebook(nb.value)).map((f) => ({
    ...f,
    folder: true,
    open: f.docs.length > 0 && (folderOpen.value[f.key] ?? f.key !== "repo"),
  }));
});
const inView = computed(() =>
  sections.value.flatMap((s) => (s.open ? s.docs : [])),
);
const drafts = (key: string) =>
  visible.value.filter((d) => d.status === "draft" && inNotebook(d, key))
    .length;
const capped = computed(() => docs.value.length === 500);
const found = computed(() => sections.value.flatMap((s) => s.docs));
const toggleFolder = (key: string, open: boolean) =>
  (folderOpen.value = { ...folderOpen.value, [key]: !open });

let seq = 0;
async function load() {
  const mine = ++seq;
  const p = new URLSearchParams();
  p.set("status", "all");
  if (q.value.trim()) p.set("q", q.value.trim());
  try {
    const res = await api.get<{ docs: KBDoc[] }>(`/api/v1/kb/docs?${p}`);
    if (mine !== seq) return;
    docs.value = res.docs ?? [];
    error.value = "";
  } catch (e) {
    if (mine !== seq) return;
    docs.value = [];
    error.value = errMsg(e);
  }
  loading.value = false;
  if (!notebooks.value.some((b) => b.key === nb.value)) nb.value = NB_ALL;
  syncOpen();
}

function syncOpen() {
  if (editState.value !== null) return;
  const id = openId.value;
  if (id && (id === pinned || found.value.some((d) => d.id === id))) return;
  openId.value = inView.value[0]?.id ?? "";
}

let debounce: ReturnType<typeof setTimeout> | undefined;
watch(q, () => {
  clearTimeout(debounce);
  debounce = setTimeout(load, 250);
});
watch(pendingIds, syncOpen);
watch(openId, () => (quote.value = ""));

onMounted(async () => {
  document.addEventListener("keydown", onKey);
  reloadList = load;
  await Promise.allSettled([factory.refreshProjects()]);
  await load();
  const linked = route.query.doc;
  if (typeof linked !== "string" || !linked) return;
  if (!picked && docs.value.some((d) => d.id === linked)) openLinked(linked);
  void router.replace({ query: { ...route.query, doc: undefined } });
});
onBeforeUnmount(() => {
  clearTimeout(debounce);
  document.removeEventListener("keydown", onKey);
  if (reloadList === load) reloadList = null;
});

const leaveOk = () =>
  editState.value !== true ||
  window.confirm("Discard your unsaved changes to this note?");

function select(id: string) {
  picked = true;
  if (id !== openId.value && !leaveOk()) return;
  pinned = "";
  openId.value = id;
  mobile.value = "read";
}
function openLinked(id: string) {
  if (!leaveOk()) return;
  pinned = id;
  openId.value = id;
  mobile.value = "read";
  clearTimeout(debounce);
  if (nb.value !== NB_ALL || q.value) {
    nb.value = NB_ALL;
    q.value = "";
    clearTimeout(debounce);
    load();
  }
}
function setNb(key: string) {
  if (key === nb.value || !leaveOk()) return;
  editState.value = null;
  nb.value = key;
  folderOpen.value = {};
  pinned = "";
  syncOpen();
}
function move(dir: 1 | -1) {
  if (editState.value !== null) return;
  const l = inView.value;
  const i = l.findIndex((d) => d.id === openId.value);
  const next = l[Math.max(0, Math.min(l.length - 1, i + dir))];
  if (!next) return;
  pinned = "";
  openId.value = next.id;
  nextTick(() =>
    document
      .querySelector(".nb-item.on")
      ?.scrollIntoView?.({ block: "nearest" }),
  );
}
function onKey(e: KeyboardEvent) {
  const t = e.target as HTMLElement | null;
  if (
    e.metaKey ||
    e.ctrlKey ||
    e.altKey ||
    t?.closest("input, textarea, select, [contenteditable], dialog")
  )
    return;
  if (e.key === "j") move(1);
  else if (e.key === "k") move(-1);
}

function toggleDiscuss(text?: string) {
  if (text) {
    quote.value = text;
    discuss.value = true;
  } else discuss.value = !discuss.value;
  mobile.value = discuss.value ? "talk" : "read";
}
function closeDiscuss() {
  discuss.value = false;
  mobile.value = "read";
}

function afterReview() {
  openId.value = "";
  pinned = "";
  mobile.value = "list";
  return load();
}

function remove(detail: KBDocDetail) {
  const { id, title } = detail.doc;
  const l = inView.value;
  const i = l.findIndex((d) => d.id === id);
  openId.value = (l[i + 1] ?? l[i - 1])?.id ?? "";
  pinned = "";
  mobile.value = "list";
  scheduleDelete(id, title, () => {
    openId.value = id;
  });
}

const toChats = (id: string) => router.push(`/chats/${id}`);
</script>
<template>
  <div class="nb-wrap">
    <h1 class="sr-only">Knowledge</h1>
    <div
      class="nb"
      :class="{ talk: discuss }"
      :data-m="mobile"
      data-test="notebook"
    >
      <aside class="nb-side" aria-label="Notebooks">
        <SearchField
          v-model="q"
          label="Search notes"
          placeholder="Search notes"
        />
        <div class="nb-books">
          <button
            v-for="b in notebooks"
            :key="b.key"
            type="button"
            class="nb-book"
            :class="{ on: nb === b.key }"
            :aria-pressed="nb === b.key"
            @click="setNb(b.key)"
          >
            <Layers v-if="b.key === NB_ALL" class="icon" />
            <BookOpen v-else class="icon" />
            <span>{{ b.label }}</span
            ><span
              v-if="drafts(b.key)"
              class="n draft"
              :aria-label="`${drafts(b.key)} drafts`"
              data-test="kb-draft-count"
              >{{ drafts(b.key) }}</span
            ><span class="n">{{ b.count }}</span>
          </button>
        </div>
        <p v-if="capped" class="small muted" data-test="kb-capped">
          Showing the first 500 documents
        </p>
        <p v-if="error" class="notice" role="alert">{{ error }}</p>
        <p v-else-if="loading" class="small muted" role="status">
          Loading notes…
        </p>
        <div v-else class="nb-list">
          <section v-for="sec in sections" :key="sec.key">
            <button
              v-if="sec.folder"
              type="button"
              class="nb-folder"
              :class="{ open: sec.open }"
              :disabled="!sec.docs.length"
              :aria-label="`${sec.label}, ${sec.docs.length} ${sec.docs.length === 1 ? 'document' : 'documents'}`"
              :aria-expanded="sec.open"
              :aria-controls="`kb-folder-${sec.key}`"
              :data-test="`kb-folder-${sec.key}`"
              @click="toggleFolder(sec.key, sec.open)"
            >
              <ChevronRight class="icon" />
              <span>{{ sec.label }}</span
              ><span class="n">{{ sec.docs.length }}</span>
            </button>
            <div
              v-show="sec.open"
              :id="sec.folder ? `kb-folder-${sec.key}` : undefined"
              role="listbox"
              :aria-label="sec.folder ? sec.label : 'Notes'"
            >
              <button
                v-for="d in sec.docs"
                :key="d.id"
                type="button"
                class="nb-item"
                :class="{ on: d.id === openId, dep: d.status === 'deprecated' }"
                role="option"
                :aria-selected="d.id === openId"
                data-test="kb-doc"
                @click="select(d.id)"
              >
                <!-- eslint-disable vue/no-v-html -- highlight() escapes first -->
                <span class="t" v-html="highlight(d.title, q)" />
                <span
                  class="s"
                  v-html="highlight(snippet(d.description, q), q)"
                />
                <!-- eslint-enable vue/no-v-html -->
                <span class="m"
                  ><span class="tag">{{ d.type || d.source }}</span
                  ><span v-if="originLabel(d)" data-test="kb-origin">{{
                    originLabel(d)
                  }}</span
                  ><span
                    v-if="d.status !== 'stable'"
                    class="nb-pill"
                    :class="d.status"
                    >{{ d.status }}</span
                  >{{
                    d.source === "repo" ? "repo" : projectName(d.project_id)
                  }}
                  · {{ when(d.indexed_at) }}</span
                >
              </button>
            </div>
          </section>
          <div v-if="!found.length" class="small muted nb-none">
            {{
              q
                ? `No notes match “${q}”.`
                : "No notes here yet. Index a repository with docs, or let the Cataloger draft some."
            }}
          </div>
        </div>
      </aside>
      <NoteReader
        v-if="openId"
        :doc-id="openId"
        :discuss-open="discuss"
        @open="openLinked"
        @back="mobile = 'list'"
        @changed="load"
        @closed="afterReview"
        @remove="remove"
        @ask="toggleDiscuss"
        @loaded="openTitle = $event"
        @editing="editState = $event"
      />
      <article v-else class="nb-read">
        <div class="nb-empty">
          <BookOpen class="icon" />
          <p>{{ loading ? "Loading…" : "Pick a note to read it." }}</p>
        </div>
      </article>
      <DiscussPanel
        v-if="discuss && openId"
        v-model:quote="quote"
        :doc-id="openId"
        :title="openTitle"
        @close="closeDiscuss"
        @chat="toChats"
      />
    </div>
  </div>
</template>
