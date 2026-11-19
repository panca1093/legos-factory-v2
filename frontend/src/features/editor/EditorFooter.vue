<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { useEditor } from "./editorStore";
import { languageLabel, SERVER_FOR } from "./monaco";

const props = defineProps<{ folderId: string }>();
const store = useEditor();
const st = computed(() => store.folders[props.folderId]);
const buf = computed(() =>
  st.value ? st.value.buffers[st.value.active] : undefined,
);
const tab = computed(() =>
  st.value?.tabs.find((t) => t.path === st.value?.active),
);
const lang = computed(() => buf.value?.language ?? "");
const server = computed(() => SERVER_FOR[lang.value]);
const serverState = computed(() =>
  server.value ? store.serverStates[server.value] : undefined,
);
const dirtyCount = computed(
  () => Object.values(st.value?.buffers ?? {}).filter((b) => b.dirty).length,
);
const saveText = computed(() => {
  const b = buf.value;
  if (b?.saving) return "Saving…";
  if (b?.banner?.kind === "conflict") return "Conflict";
  if (dirtyCount.value) return `Unsaved (${dirtyCount.value})`;
  return "Saved";
});
const counts = computed(() => {
  let errors = 0,
    warnings = 0;
  for (const [uri, list] of store.diagnostics) {
    const loc = store.locate(uri);
    if (loc?.folderId !== props.folderId) continue;
    for (const d of list) {
      if (d.severity === 1) errors++;
      else if (d.severity === 2) warnings++;
    }
  }
  return { errors, warnings };
});
function problems() {
  window.dispatchEvent(new Event("legos:show-panel"));
  setTimeout(
    () =>
      document
        .querySelector('[data-card="problems"]')
        ?.scrollIntoView({ block: "nearest" }),
    50,
  );
}
</script>

<template>
  <footer v-if="buf" class="ef" data-test="editor-footer">
    <span data-test="footer-language">{{ languageLabel(lang) }}</span>
    <RouterLink
      v-if="server"
      class="ef-server"
      :class="{
        warn:
          !serverState ||
          ['not_found', 'crashed', 'outdated', 'restricted'].includes(
            serverState.state,
          ),
      }"
      to="/settings#editor"
      data-test="footer-server"
      >{{ server }}
      {{
        serverState
          ? serverState.state.replace("_", " ") +
            (serverState.progress != null ? ` ${serverState.progress}%` : "")
          : "no language server"
      }}</RouterLink
    >
    <span data-test="footer-cursor"
      >Ln {{ tab?.cursor?.line ?? 1 }}, Col {{ tab?.cursor?.col ?? 1 }}</span
    >
    <span>UTF-8{{ buf.bom ? " BOM" : "" }} · {{ buf.eol }}</span>
    <button
      v-if="counts.errors || counts.warnings"
      type="button"
      class="ef-count"
      data-test="footer-problems"
      @click="problems"
    >
      <span v-if="counts.errors" class="err">{{ counts.errors }} errors</span>
      <span v-if="counts.warnings" class="warn"
        >{{ counts.warnings }} warnings</span
      >
    </button>
    <span
      class="ef-save"
      :class="{
        dirty: dirtyCount > 0,
        conflict: buf.banner?.kind === 'conflict',
      }"
      data-test="footer-save"
      >{{ saveText }}</span
    >
  </footer>
</template>

<style scoped>
.ef {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  padding: 8px 18px;
  border-top: 1px solid var(--line);
  background: var(--canvas);
  color: var(--muted);
  font-size: 12px;
}
.ef-server.warn {
  color: var(--warning);
}
.ef-count {
  display: inline-flex;
  gap: 8px;
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  cursor: pointer;
}
.err {
  color: var(--danger);
}
.warn {
  color: var(--warning);
}
.ef-save {
  margin-left: auto;
}
.ef-save.dirty {
  color: var(--warning);
}
.ef-save.conflict {
  color: var(--danger);
}
</style>
