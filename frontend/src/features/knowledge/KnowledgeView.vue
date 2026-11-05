<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import { useRouter } from "vue-router";
import { BookOpen, Layers } from "lucide-vue-next";
import SearchField from "../../components/ui/SearchField.vue";
import { useToast } from "../../composables/useToast";
import { api } from "../../services/api/client";
import { useFactory } from "../../stores/factory";
import type { KBDoc, KBDocDetail } from "../../types/api";
import DiscussPanel from "./DiscussPanel.vue";
import NoteReader from "./NoteReader.vue";
import {
  highlight,
  inNotebook,
  NB_ALL,
  NB_DRAFTS,
  notebooksOf,
  snippet,
  when,
} from "./notes";
import "./knowledge.css";

const factory = useFactory();
const router = useRouter();
const { toast } = useToast();

const docs = ref<KBDoc[]>([]),
  loading = ref(true),
  error = ref("");
const q = ref(""),
  nb = ref(NB_ALL),
  openId = ref(""),
  discuss = ref(false),
  quote = ref(""),
  mobile = ref<"list" | "read" | "talk">("list"),
  pending = ref(new Set<string>());

const idQuery = (id: string) => `id=${encodeURIComponent(id)}`;
const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));
const projectName = (id?: string) =>
  !id
    ? "Factory-wide"
    : (factory.projects.find((p) => p.id === id)?.name ?? id);
const ts = (d: KBDoc) =>
  typeof d.indexed_at === "number" ? d.indexed_at : Date.parse(d.indexed_at);

const visible = computed(() =>
  docs.value.filter((d) => !pending.value.has(d.id)),
);
const notebooks = computed(() => notebooksOf(visible.value, projectName));
const list = computed(() =>
  visible.value
    .filter((d) => inNotebook(d, nb.value))
    .sort((a, b) => ts(b) - ts(a)),
);
const openDoc = computed(() => list.value.find((d) => d.id === openId.value));

async function load() {
  error.value = "";
  const p = new URLSearchParams({ status: "all" });
  if (q.value.trim()) p.set("q", q.value.trim());
  try {
    const res = await api.get<{ docs: KBDoc[] }>(`/api/v1/kb/docs?${p}`);
    docs.value = res.docs ?? [];
  } catch (e) {
    docs.value = [];
    error.value = errMsg(e);
  } finally {
    loading.value = false;
  }
}

let debounce: ReturnType<typeof setTimeout> | undefined;
watch(q, () => {
  clearTimeout(debounce);
  debounce = setTimeout(load, 250);
});
watch(
  [list, nb],
  () => {
    if (!list.value.some((d) => d.id === openId.value))
      openId.value = list.value[0]?.id ?? "";
  },
  { flush: "sync" },
);
watch(openId, () => (quote.value = ""));

onMounted(async () => {
  document.addEventListener("keydown", onKey);
  await Promise.allSettled([factory.refreshProjects()]);
  await load();
});
onBeforeUnmount(() => {
  clearTimeout(debounce);
  document.removeEventListener("keydown", onKey);
});

function select(id: string) {
  openId.value = id;
  mobile.value = "read";
}
function move(dir: 1 | -1) {
  const i = list.value.findIndex((d) => d.id === openId.value);
  const next =
    list.value[Math.max(0, Math.min(list.value.length - 1, i + dir))];
  if (!next) return;
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
  mobile.value = "list";
  return load();
}

function remove(detail: KBDocDetail) {
  const { id, title } = detail.doc;
  const i = list.value.findIndex((d) => d.id === id);
  openId.value = (list.value[i + 1] ?? list.value[i - 1])?.id ?? "";
  pending.value = new Set(pending.value).add(id);
  mobile.value = "list";
  const restore = () => {
    const s = new Set(pending.value);
    s.delete(id);
    pending.value = s;
  };
  toast(
    `Deleted “${title}”`,
    () => {
      restore();
      openId.value = id;
    },
    async () => {
      try {
        await api.del(`/api/v1/kb/doc?${idQuery(id)}`);
        await load();
        restore();
      } catch (e) {
        restore();
        toast(`Could not delete “${title}”: ${errMsg(e)}`);
        await load();
      }
    },
  );
}

const toChats = (id: string) => router.push(`/chats/${id}`);
</script>
<template>
  <div class="nb-wrap">
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
            :class="{ on: nb === b.key, drafts: b.key === NB_DRAFTS }"
            :aria-pressed="nb === b.key"
            :data-test="b.key === NB_DRAFTS ? 'drafts-shortcut' : undefined"
            @click="nb = b.key"
          >
            <Layers v-if="b.key === NB_ALL" class="icon" />
            <BookOpen v-else class="icon" />
            <span>{{ b.label }}</span
            ><span class="n">{{ b.count }}</span>
          </button>
        </div>
        <p v-if="error" class="notice" role="alert">{{ error }}</p>
        <p v-else-if="loading" class="small muted" role="status">
          Loading notes…
        </p>
        <div v-else class="nb-list" role="listbox" aria-label="Notes">
          <button
            v-for="d in list"
            :key="d.id"
            type="button"
            class="nb-item"
            :class="{ on: d.id === openId }"
            role="option"
            :aria-selected="d.id === openId"
            data-test="kb-doc"
            @click="select(d.id)"
          >
            <!-- eslint-disable vue/no-v-html -- highlight() escapes first -->
            <span class="t" v-html="highlight(d.title, q)" />
            <span class="s" v-html="highlight(snippet(d.description, q), q)" />
            <!-- eslint-enable vue/no-v-html -->
            <span class="m"
              ><span class="tag">{{ d.type || "untyped" }}</span
              >{{ d.source === "repo" ? "repo" : projectName(d.project_id) }} ·
              {{ when(d.indexed_at) }}</span
            >
          </button>
          <div v-if="!list.length" class="small muted nb-none">
            {{
              q
                ? `No notes match “${q}”.`
                : "No notes here yet. Index a repository with docs, or let the Cataloger draft some."
            }}
          </div>
        </div>
      </aside>
      <NoteReader
        v-if="openDoc"
        :doc-id="openDoc.id"
        :discuss-open="discuss"
        @open="select"
        @back="mobile = 'list'"
        @changed="load"
        @closed="afterReview"
        @remove="remove"
        @ask="toggleDiscuss"
      />
      <article v-else class="nb-read">
        <div class="nb-empty">
          <BookOpen class="icon" />
          <p>{{ loading ? "Loading…" : "Pick a note to read it." }}</p>
        </div>
      </article>
      <DiscussPanel
        v-if="discuss && openDoc"
        v-model:quote="quote"
        :doc-id="openDoc.id"
        :title="openDoc.title"
        @close="closeDiscuss"
        @chat="toChats"
      />
    </div>
  </div>
</template>
