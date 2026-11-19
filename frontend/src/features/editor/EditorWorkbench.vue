<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { RouterLink, useRouter } from "vue-router";
import MonacoHost from "./MonacoHost.vue";
import MonacoDiff from "./MonacoDiff.vue";
import EditorTabs from "./EditorTabs.vue";
import EditorFooter from "./EditorFooter.vue";
import ChangedBanner from "./ChangedBanner.vue";
import QuickOpen from "./QuickOpen.vue";
import { useEditor } from "./editorStore";
import { loadMonaco } from "./monaco";

const props = defineProps<{ prHead?: string }>();
const store = useEditor();
const router = useRouter();
const fid = computed(() => store.currentId);
const st = computed(() => store.folders[fid.value]);
const active = computed(() => st.value?.active ?? "");
const buf = computed(() =>
  active.value ? st.value?.buffers[active.value] : undefined,
);
const model = computed(() =>
  buf.value?.status === "ready"
    ? (store.modelOf(fid.value, active.value) ?? null)
    : null,
);
const tab = computed(() => st.value?.tabs.find((t) => t.path === active.value));
const reveal = computed(() =>
  store.reveal &&
  store.reveal.folderId === fid.value &&
  store.reveal.path === active.value
    ? store.reveal
    : null,
);
const dirtyCount = computed(
  () => Object.values(st.value?.buffers ?? {}).filter((b) => b.dirty).length,
);
const stripDismissed = ref(false);
const branch = computed(
  () => st.value?.git?.branch || st.value?.folder.branch || "",
);
const mismatch = computed(
  () =>
    !!props.prHead &&
    !!branch.value &&
    branch.value !== props.prHead &&
    !stripDismissed.value,
);

const crumbs = computed(() => {
  const parts = active.value.split("/").filter(Boolean);
  return parts.map((name, i) => ({
    name,
    path: parts.slice(0, i + 1).join("/"),
    parent: parts.slice(0, i).join("/"),
    last: i === parts.length - 1,
  }));
});
const openCrumb = ref("");
const siblings = (parent: string) => st.value?.listings[parent] ?? [];
function pickSibling(parent: string, e: { name: string; kind: string }) {
  openCrumb.value = "";
  const p = parent ? `${parent}/${e.name}` : e.name;
  if (e.kind === "dir") void store.revealInTree(fid.value, `${p}/x`);
  else void store.openFile(fid.value, p, { pin: true });
}

const diskCopy = ref<string | null>(null);
watch(
  () => buf.value?.banner?.comparing,
  async (on) => {
    diskCopy.value = null;
    if (!on) return;
    try {
      diskCopy.value = await store.diskCopy(fid.value, active.value);
    } catch {
      store.setComparing(fid.value, active.value, false);
    }
  },
);
const comparing = computed(
  () =>
    !!buf.value?.banner?.comparing && diskCopy.value !== null && !!model.value,
);

function recreate() {
  store.keepMine(fid.value, active.value);
  void store.save(fid.value, active.value);
}
const bannerName = computed(() => active.value.split("/").pop() ?? "");
function key(e: KeyboardEvent) {
  const mod = e.metaKey || e.ctrlKey;
  if (!mod) return;
  const k = e.key.toLowerCase();
  if (k === "s") {
    e.preventDefault();
    if (e.altKey) void store.saveAll(fid.value);
    else if (active.value) void store.save(fid.value, active.value);
  } else if (k === "p" && !e.shiftKey) {
    e.preventDefault();
    store.quickOpen = true;
  } else if (k === "w" && active.value) {
    e.preventDefault();
    store.requestClose(fid.value, active.value);
  }
}

const lspModules = import.meta.glob<{
  connectLanguageServers: (s: unknown) => unknown;
}>("./lsp.ts");
let disposeLsp: (() => void) | null = null;
let unmounted = false;

onMounted(async () => {
  document.addEventListener("keydown", key);
  try {
    await loadMonaco().then((m) => (store.monaco = m));
  } catch {
    return;
  }
  const load = lspModules["./lsp.ts"];
  if (!load || unmounted) return;
  try {
    const mod = await load();
    const d = mod.connectLanguageServers(store);
    if (typeof d === "function") disposeLsp = d as () => void;
    if (unmounted) disposeLsp?.();
  } catch {
    /* The language client is optional. */
  }
});
onBeforeUnmount(() => {
  unmounted = true;
  document.removeEventListener("keydown", key);
  try {
    disposeLsp?.();
  } catch {
    /* A failing disposer must not block unmount. */
  }
});

const sizeText = (n?: number) =>
  n ? `${(n / (1024 * 1024)).toFixed(1)} MB` : "unknown size";
function backToProject() {
  void router.push(
    `/projects/${st.value?.folder.project_id ?? store.workspaceProject}/code`,
  );
}
</script>

<template>
  <div class="ew" data-test="editor-workbench">
    <div v-if="st?.status === 'gone'" class="ew-state" data-test="folder-gone">
      <h2>This folder no longer exists</h2>
      <p class="help">
        It was removed, for example after its work order merged.
      </p>
      <div class="ew-actions">
        <RouterLink
          v-if="st.folder.wo_id"
          class="lg-btn primary"
          :to="`/projects/${st.folder.project_id || store.workspaceProject}/wo/${st.folder.wo_id}`"
          >Open the work order</RouterLink
        >
        <button type="button" class="lg-btn" @click="backToProject">
          Close
        </button>
      </div>
    </div>
    <template v-else-if="st">
      <p
        v-if="st.folder.kind === 'worktree'"
        class="ew-strip"
        data-test="wo-strip"
      >
        Edits here ship with work order
        <strong class="mono">{{ st.folder.wo_id }}</strong> if it commits them.
      </p>
      <p v-if="mismatch" class="ew-strip warn" data-test="branch-strip">
        Your folder is on <strong class="mono">{{ branch }}</strong>; the PR is
        on <strong class="mono">{{ prHead }}</strong>.
        <button
          type="button"
          class="lg-btn small ghost"
          @click="stripDismissed = true"
        >
          Dismiss
        </button>
      </p>
      <div class="ew-card">
        <EditorTabs :folder-id="fid" />
        <nav v-if="active" class="ew-crumbs" aria-label="File path">
          <span v-for="c in crumbs" :key="c.path" class="ew-crumb">
            <button
              type="button"
              class="ew-cbtn mono"
              :class="{ last: c.last }"
              @click="openCrumb = openCrumb === c.path ? '' : c.path"
            >
              {{ c.name }}
            </button>
            <span v-if="!c.last" class="sep">›</span>
            <ul v-if="openCrumb === c.path" class="ew-drop" role="menu">
              <li v-for="e in siblings(c.parent)" :key="e.name">
                <button
                  type="button"
                  role="menuitem"
                  class="mono"
                  @click="pickSibling(c.parent, e)"
                >
                  {{ e.name }}{{ e.kind === "dir" ? "/" : "" }}
                </button>
              </li>
              <li v-if="!siblings(c.parent).length" class="muted small">
                Not loaded
              </li>
            </ul>
          </span>
          <span v-if="buf?.readonly" class="ew-ro" data-test="readonly-badge"
            >Read-only</span
          >
        </nav>
        <ChangedBanner
          v-if="buf?.banner && buf.status === 'ready'"
          :banner="buf.banner"
          :name="bannerName"
          :dirty="buf.dirty"
          @reload="store.reloadBuffer(fid, active)"
          @keep="store.keepMine(fid, active)"
          @compare="store.setComparing(fid, active, true)"
          @recreate="recreate"
          @close="store.closeTab(fid, active)"
        />
        <div v-if="comparing && model && buf" class="ew-body">
          <div class="ew-comparebar">
            <span class="small">Left: on disk · Right: yours (editable)</span>
            <button
              type="button"
              class="lg-btn small"
              data-test="compare-close"
              @click="store.setComparing(fid, active, false)"
            >
              Back to the editor
            </button>
          </div>
          <MonacoDiff
            :disk="diskCopy!"
            :model="model"
            :language="buf.language"
          />
        </div>
        <div v-else class="ew-body">
          <div v-if="!active" class="ew-empty" data-test="editor-empty">
            <p>Pick a file in the Explorer, or press <kbd>⌘P</kbd>.</p>
          </div>
          <div
            v-else-if="buf?.status === 'placeholder'"
            class="ew-empty"
            data-test="editor-placeholder"
          >
            <p v-if="buf.placeholder?.code === 413">
              This file is {{ sizeText(buf.placeholder.size) }}, too large to
              open here (limit 10 MB).
            </p>
            <p v-else>
              Binary file{{
                buf.placeholder?.size
                  ? ` · ${sizeText(buf.placeholder.size)}`
                  : ""
              }}, not shown.
            </p>
          </div>
          <div
            v-else-if="buf?.status === 'error'"
            class="ew-empty"
            role="alert"
          >
            <p>Could not open this file. {{ buf.error }}</p>
            <button
              type="button"
              class="lg-btn small"
              @click="store.closeTab(fid, active)"
            >
              Close tab
            </button>
          </div>
          <p v-else-if="!model" class="ew-empty" data-test="editor-loading">
            Loading…
          </p>
          <MonacoHost
            v-if="store.monaco"
            v-show="model && !comparing"
            :model="model"
            :read-only="!!buf?.readonly"
            :reveal="reveal"
            :cursor="tab?.cursor"
            @cursor="(l, c) => store.setCursor(fid, active, l, c)"
            @save="store.save(fid, active)"
            @save-all="store.saveAll(fid)"
            @quick="store.quickOpen = true"
            @close-tab="store.requestClose(fid, active)"
          />
        </div>
        <EditorFooter :folder-id="fid" />
      </div>
      <span class="sr-only" aria-live="polite">{{
        dirtyCount ? `${dirtyCount} unsaved` : ""
      }}</span>
    </template>
    <p v-else class="ew-state muted" data-test="editor-wait">
      Opening the folder…
    </p>
    <QuickOpen v-if="store.quickOpen && st?.status === 'ready'" />
  </div>
</template>

<style scoped>
.ew {
  display: grid;
  gap: var(--space-3);
  min-width: 0;
}
.ew-card {
  display: flex;
  flex-direction: column;
  height: max(460px, calc(100dvh - 230px));
  overflow: hidden;
  border: 1px solid var(--line);
  border-radius: var(--radius-xl);
  background: var(--surface);
  box-shadow: var(--shadow-card);
}
.ew-body {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  display: grid;
  grid-template-rows: minmax(0, 1fr);
}
.ew-body > * {
  grid-area: 1 / 1;
}
.ew-comparebar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 14px;
  background: var(--surface-2);
  z-index: 1;
  grid-area: auto;
}
.ew-body:has(.ew-comparebar) {
  grid-template-rows: auto minmax(0, 1fr);
}
.ew-empty,
.ew-state {
  display: grid;
  place-content: center;
  justify-items: center;
  gap: var(--space-2);
  padding: var(--space-6);
  color: var(--muted);
  text-align: center;
}
.ew-state {
  border: 1px solid var(--line);
  border-radius: var(--radius-xl);
  background: var(--surface);
}
.ew-actions {
  display: flex;
  gap: var(--space-2);
}
.ew-strip {
  margin: 0;
  padding: 8px 14px;
  border-radius: var(--radius-md);
  background: var(--info-soft);
  color: var(--info);
  font-size: 13px;
}
.ew-strip.warn {
  background: var(--warning-soft);
  color: var(--warning);
}
.ew-crumbs {
  position: relative;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 2px;
  padding: 4px 14px;
  border-bottom: 1px solid var(--line);
  font-size: 12px;
  color: var(--muted);
}
.ew-crumb {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 2px;
}
.ew-cbtn {
  padding: 2px 4px;
  border: 0;
  border-radius: var(--radius-xs);
  background: none;
  color: var(--muted);
  font-size: 12px;
  cursor: pointer;
}
.ew-cbtn.last {
  color: var(--ink);
  font-weight: 600;
}
.ew-cbtn:hover {
  background: var(--surface-2);
}
.ew-drop {
  position: absolute;
  top: 100%;
  left: 0;
  z-index: 20;
  max-height: 260px;
  min-width: 160px;
  margin: 2px 0 0;
  padding: 4px;
  overflow-y: auto;
  list-style: none;
  border: 1px solid var(--line);
  border-radius: var(--radius-md);
  background: var(--surface);
  box-shadow: var(--shadow-pop);
}
.ew-drop button {
  display: block;
  width: 100%;
  padding: 4px 8px;
  border: 0;
  border-radius: var(--radius-xs);
  background: none;
  color: var(--ink);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.ew-drop button:hover {
  background: var(--surface-2);
}
.ew-ro {
  margin-left: auto;
  padding: 1px 8px;
  border-radius: var(--radius-full);
  background: var(--surface-2);
  font-weight: 600;
}
</style>
