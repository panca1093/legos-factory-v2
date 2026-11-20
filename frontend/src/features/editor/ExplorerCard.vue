<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import { FilePlus, FolderPlus, GitBranch, Search } from "lucide-vue-next";
import PanelCard from "../projects/PanelCard.vue";
import FileTree from "../../components/files/FileTree.vue";
import {
  dirMarker,
  fileMarker,
  flattenTree,
  type TreeRow,
} from "../../components/files/tree";
import { useToast } from "../../composables/useToast";
import { useEditor } from "./editorStore";

const store = useEditor(),
  { toast } = useToast();
const st = computed(() => store.current());
const fid = computed(() => store.currentId);
const rows = computed<TreeRow[]>(() => {
  const s = st.value;
  if (!s) return [];
  if (onlyChanged.value && s.markers)
    return [...s.markers.files.keys()].sort().map((path) => ({
      path,
      tail: path,
      label: path,
      depth: 0,
      kind: "file" as const,
      open: false,
      ignored: false,
    }));
  return flattenTree((d) => s.listings[d], new Set(s.expanded));
});
const changed = computed(() => st.value?.git?.entries.length ?? 0);
const onlyChanged = ref(false);
const truncated = computed(() =>
  Object.values(st.value?.truncated ?? {}).reduce((a, b) => a + b, 0),
);
const marker = (row: TreeRow) => {
  const m = st.value?.markers;
  if (!m) return undefined;
  return row.kind === "dir" ? dirMarker(m, row.path) : fileMarker(m, row.path);
};

const active = computed(() => st.value?.active ?? "");
const selectedDir = ref("");

function toggle(row: TreeRow) {
  selectedDir.value = row.path;
  void store.toggleDir(fid.value, row.path);
}
function select(row: TreeRow, pin: boolean) {
  selectedDir.value = row.path.includes("/")
    ? row.path.slice(0, row.path.lastIndexOf("/"))
    : "";
  void store.openFile(fid.value, row.path, { pin });
}

type Op = { kind: "file" | "folder" | "rename"; base: string; from?: string };
const op = ref<Op | null>(null);
const name = ref("");
const input = ref<HTMLInputElement>();
const busy = ref(false);
function begin(kind: Op["kind"], base = selectedDir.value, from?: string) {
  menu.value = null;
  op.value = { kind, base, from };
  name.value = from ? from.split("/").pop()! : "";
  void nextTick(() => {
    input.value?.focus();
    input.value?.select();
  });
}
async function commit() {
  const o = op.value;
  const n = name.value.trim();
  if (!o || !n || busy.value) return;
  if (n.includes("..") || n.startsWith("/"))
    return toast("Use a relative name.");
  busy.value = true;
  try {
    if (o.kind === "rename") {
      const dir = o.from!.includes("/")
        ? o.from!.slice(0, o.from!.lastIndexOf("/"))
        : "";
      await store.renamePath(fid.value, o.from!, dir ? `${dir}/${n}` : n);
    } else {
      const path = o.base ? `${o.base}/${n}` : n;
      if (o.kind === "file") await store.createFile(fid.value, path);
      else await store.createFolder(fid.value, path);
    }
    op.value = null;
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  } finally {
    busy.value = false;
  }
}

const menu = ref<{ row: TreeRow; x: number; y: number } | null>(null);
function openMenu(row: TreeRow, ev: MouseEvent) {
  menu.value = { row, x: ev.clientX, y: ev.clientY };
}
async function copyPath(p: string) {
  menu.value = null;
  try {
    await navigator.clipboard.writeText(p);
    toast("Path copied");
  } catch {
    toast(p);
  }
}
const baseOf = (row: TreeRow) =>
  row.kind === "dir"
    ? row.path
    : row.path.includes("/")
      ? row.path.slice(0, row.path.lastIndexOf("/"))
      : "";
</script>

<template>
  <PanelCard id="explorer" title="Explorer">
    <div v-if="st" class="ex" data-test="explorer">
      <div class="ex-head">
        <span class="mono ex-branch" :title="st.folder.path"
          ><GitBranch class="icon" />{{
            st.git?.branch || st.folder.branch || "no branch"
          }}</span
        >
        <button
          v-if="changed"
          type="button"
          class="ex-changed"
          :class="{ on: onlyChanged }"
          :aria-pressed="onlyChanged"
          data-test="explorer-changed"
          @click="onlyChanged = !onlyChanged"
        >
          {{ changed }} changed
        </button>
        <span class="ex-grow"></span>
        <button
          type="button"
          class="ex-ico"
          title="New file"
          aria-label="New file"
          @click="begin('file')"
        >
          <FilePlus class="icon" /></button
        ><button
          type="button"
          class="ex-ico"
          title="New folder"
          aria-label="New folder"
          @click="begin('folder')"
        >
          <FolderPlus class="icon" />
        </button>
      </div>
      <button
        type="button"
        class="ex-find"
        data-test="explorer-find"
        @click="store.quickOpen = true"
      >
        <Search class="icon" />Find files…<kbd>⌘P</kbd>
      </button>
      <form v-if="op" class="ex-op" @submit.prevent="commit">
        <label class="small muted" for="ex-name">{{
          op.kind === "rename"
            ? "Rename"
            : `New ${op.kind} in ${op.base || "root"}`
        }}</label>
        <input
          id="ex-name"
          ref="input"
          v-model="name"
          class="ex-input"
          autocomplete="off"
          spellcheck="false"
          :disabled="busy"
          @keydown.esc.prevent="op = null"
        />
      </form>
      <p v-if="st.status === 'error'" class="small ex-err" role="alert">
        {{ st.error }}
      </p>
      <FileTree
        :rows="rows"
        :selected="active"
        :height="Math.min(Math.max(rows.length * 24, 48), 440)"
        @toggle="toggle"
        @select="select"
        @menu="openMenu"
      >
        <template #marker="{ row }">
          <span
            v-if="marker(row)"
            class="ex-mk"
            :class="[`m-${marker(row)}`, { dot: row.kind === 'dir' }]"
            data-test="tree-marker"
            >{{ row.kind === "dir" ? "●" : marker(row) }}</span
          >
        </template>
      </FileTree>
      <p v-if="truncated" class="small muted">
        {{ truncated }} more entries not shown
      </p>
      <div
        v-if="menu"
        class="ex-menu-scrim"
        @click="menu = null"
        @contextmenu.prevent="menu = null"
      >
        <ul
          class="ex-menu"
          :style="{ left: `${menu.x}px`, top: `${menu.y}px` }"
          role="menu"
          @click.stop
        >
          <li>
            <button
              type="button"
              role="menuitem"
              @click="begin('file', baseOf(menu.row))"
            >
              New file
            </button>
          </li>
          <li>
            <button
              type="button"
              role="menuitem"
              @click="begin('folder', baseOf(menu.row))"
            >
              New folder
            </button>
          </li>
          <li>
            <button
              type="button"
              role="menuitem"
              @click="begin('rename', '', menu.row.path)"
            >
              Rename
            </button>
          </li>
          <li>
            <button
              type="button"
              role="menuitem"
              @click="copyPath(menu.row.path)"
            >
              Copy relative path
            </button>
          </li>
        </ul>
      </div>
    </div>
    <p v-else class="small muted">Open a folder to browse its files.</p>
  </PanelCard>
</template>

<style scoped>
.ex {
  display: grid;
  gap: var(--space-2);
  min-width: 0;
}
.ex-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: 12px;
  color: var(--muted);
}
.ex-branch {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ex-grow {
  flex: 1;
}
.ex-changed {
  padding: 1px 8px;
  border: 0;
  border-radius: var(--radius-full);
  background: var(--warning-soft);
  color: var(--warning);
  font: 600 11px var(--font-sans);
  cursor: pointer;
}
.ex-changed.on {
  box-shadow: 0 0 0 2px var(--warning);
}
.ex-ico {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border: 0;
  border-radius: var(--radius-xs);
  background: none;
  color: var(--muted);
  cursor: pointer;
}
.ex-ico:hover {
  background: var(--surface-2);
  color: var(--ink);
}
.ex-find {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  height: var(--control-height-sm);
  padding: 0 12px;
  border: 1px solid var(--line);
  border-radius: var(--radius-md);
  background: var(--surface);
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
}
.ex-find kbd {
  margin-left: auto;
  font: 11px var(--font-mono);
}
.ex-op {
  display: grid;
  gap: 4px;
}
.ex-input {
  height: var(--control-height-sm);
  padding: 0 10px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--ink);
  font: 13px var(--font-mono);
}
.ex-err {
  color: var(--danger);
}
.ex-mk {
  font: 700 11px var(--font-mono);
  color: var(--warning);
}
.ex-mk.m-U,
.ex-mk.m-A {
  color: var(--success);
}
.ex-mk.m-D,
.ex-mk.m-\! {
  color: var(--danger);
}
.ex-mk.dot {
  font-size: 8px;
}
.ex-menu-scrim {
  position: fixed;
  inset: 0;
  z-index: 60;
}
.ex-menu {
  position: fixed;
  margin: 0;
  padding: 4px;
  list-style: none;
  min-width: 170px;
  border: 1px solid var(--line);
  border-radius: var(--radius-md);
  background: var(--surface);
  box-shadow: var(--shadow-pop);
}
.ex-menu button {
  display: block;
  width: 100%;
  padding: 6px 10px;
  border: 0;
  border-radius: var(--radius-xs);
  background: none;
  color: var(--ink);
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}
.ex-menu button:hover {
  background: var(--surface-2);
}
</style>
