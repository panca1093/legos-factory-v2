<script setup lang="ts">
import { computed } from "vue";
import type { Diagnostic } from "vscode-languageserver-protocol/browser";
import PanelCard from "../projects/PanelCard.vue";
import { fileUriToPath } from "./lsp";

const props = defineProps<{
  diagnostics: Map<string, Diagnostic[]>;
  folderPath?: string;
}>();
const emit = defineEmits<{
  open: [{ uri: string; line: number; column: number }];
}>();

const label = ["", "Error", "Warning", "Info", "Hint"];
const groups = computed(() =>
  [...props.diagnostics.entries()]
    .filter(([, list]) => list.length)
    .map(([uri, list]) => {
      const path = fileUriToPath(uri);
      const root = props.folderPath
        ? props.folderPath.replace(/\/$/, "") + "/"
        : "";
      return {
        uri,
        name: root && path.startsWith(root) ? path.slice(root.length) : path,
        list: [...list].sort(
          (a, b) =>
            (a.severity ?? 1) - (b.severity ?? 1) ||
            a.range.start.line - b.range.start.line,
        ),
      };
    })
    .sort((a, b) => a.name.localeCompare(b.name)),
);
const total = computed(() =>
  groups.value.reduce((n, g) => n + g.list.length, 0),
);
</script>
<template>
  <PanelCard id="problems" :title="total ? `Problems (${total})` : 'Problems'">
    <p v-if="!groups.length" class="help" data-test="no-problems">
      No problems in open files.
    </p>
    <div
      v-for="g in groups"
      :key="g.uri"
      class="prob-file"
      data-test="prob-file"
    >
      <div class="prob-name">
        {{ g.name }} <span class="mono">{{ g.list.length }}</span>
      </div>
      <button
        v-for="(d, i) in g.list"
        :key="i"
        type="button"
        class="prob-row"
        :data-severity="d.severity ?? 1"
        @click="
          emit('open', {
            uri: g.uri,
            line: d.range.start.line + 1,
            column: d.range.start.character + 1,
          })
        "
      >
        <span class="prob-sev">{{ label[d.severity ?? 1] }}</span>
        <span class="prob-msg">{{ d.message }}</span>
        <span class="mono"
          >{{ d.range.start.line + 1 }}:{{ d.range.start.character + 1 }}</span
        >
      </button>
    </div>
  </PanelCard>
</template>
<style scoped>
.prob-file + .prob-file {
  margin-top: 8px;
}
.prob-name {
  font-size: 12px;
  font-weight: 600;
  overflow-wrap: anywhere;
}
.prob-row {
  display: grid;
  grid-template-columns: auto 1fr auto;
  gap: 8px;
  width: 100%;
  padding: 4px 6px;
  text-align: left;
  font-size: 12px;
  border-radius: var(--radius-md);
}
.prob-row:hover {
  background: var(--surface-2);
}
.prob-row[data-severity="1"] .prob-sev {
  color: var(--danger);
}
.prob-sev {
  color: var(--muted);
}
.prob-msg {
  overflow-wrap: anywhere;
}
</style>
