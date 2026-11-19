<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import AppDialog from "../../components/ui/AppDialog.vue";
import { useEditor } from "./editorStore";
import { rankPaths } from "./fuzzy";

const store = useEditor();
const query = ref("");
const paths = ref<string[]>([]);
const sel = ref(0);
const input = ref<HTMLInputElement>();

const results = computed(() => rankPaths(query.value, paths.value));
watch(query, () => (sel.value = 0));
onMounted(async () => {
  paths.value = await store.loadQuick(store.currentId);
  await nextTick();
  input.value?.focus();
});
function close() {
  store.quickOpen = false;
}
function pick(path: string | undefined) {
  if (!path) return;
  close();
  void store.openFile(store.currentId, path);
}
function key(e: KeyboardEvent) {
  if (e.key === "ArrowDown") {
    e.preventDefault();
    sel.value = Math.min(sel.value + 1, results.value.length - 1);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    sel.value = Math.max(sel.value - 1, 0);
  } else if (e.key === "Enter") {
    e.preventDefault();
    pick(results.value[sel.value]);
  }
}
</script>

<template>
  <AppDialog :open="true" title="Find files" @close="close">
    <div class="qo" data-test="quick-open">
      <input
        ref="input"
        v-model="query"
        class="qo-input"
        placeholder="Type a file name"
        aria-label="File name"
        autocomplete="off"
        spellcheck="false"
        @keydown="key"
      />
      <ul class="qo-list" role="listbox">
        <li
          v-for="(p, i) in results"
          :key="p"
          role="option"
          :aria-selected="i === sel"
          :class="{ on: i === sel }"
          @click="pick(p)"
          @mousemove="sel = i"
        >
          <span class="qo-name">{{ p.split("/").pop() }}</span>
          <span class="qo-dir mono">{{
            p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : ""
          }}</span>
        </li>
        <li v-if="!results.length" class="qo-empty">No matching files.</li>
      </ul>
    </div>
  </AppDialog>
</template>

<style scoped>
.qo {
  display: grid;
  gap: var(--space-2);
}
.qo-input {
  height: var(--control-height);
  padding: 0 12px;
  border: 1px solid var(--line);
  border-radius: var(--radius-md);
  background: var(--surface);
  color: var(--ink);
  font: 14px var(--font-mono);
}
.qo-list {
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 340px;
  overflow-y: auto;
}
.qo-list li {
  display: flex;
  gap: var(--space-3);
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  cursor: pointer;
}
.qo-list li.on {
  background: var(--tint);
}
.qo-dir {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--muted);
  font-size: 12px;
}
.qo-empty {
  color: var(--muted);
}
</style>
