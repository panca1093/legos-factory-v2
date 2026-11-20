<script setup lang="ts">
import { computed } from "vue";
import { useEditor } from "./editorStore";
import { languageLabel } from "./monaco";

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
</script>

<template>
  <footer v-if="buf" class="ef" data-test="editor-footer">
    <span data-test="footer-language">{{ languageLabel(lang) }}</span>
    <span data-test="footer-cursor"
      >Ln {{ tab?.cursor?.line ?? 1 }}, Col {{ tab?.cursor?.col ?? 1 }}</span
    >
    <span>UTF-8{{ buf.bom ? " BOM" : "" }} · {{ buf.eol }}</span>
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
