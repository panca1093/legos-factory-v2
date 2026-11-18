<script setup lang="ts">
import { CircleCheck } from "lucide-vue-next";
import type { WorkOrder, Task } from "../../../types/api";
import type { DocSection } from "../runs/activityThread";
import { renderMarkdown } from "../../chats/chatUtils";
defineProps<{
  section: DocSection;
  order: WorkOrder;
  tasks: Task[];
  text?: string;
}>();
// Agent prose marks sections as "Domain invariants:" on its own line and leads with "Why: …"; lift those into headings and bold labels.
const md = (src?: string | null) =>
  renderMarkdown(
    (src ?? "")
      .replace(/^([A-Z][^:\n`]{1,48}):[ \t]*$/gm, "#### $1")
      .replace(/^([A-Z][A-Za-z ]{1,24}):[ \t]+(?=\S)/gm, "**$1:** "),
  );
</script>
<template>
  <div
    v-if="section === 'decision'"
    class="md"
    v-html="md(order.clarif_answer)"
  ></div>
  <div
    v-else-if="section === 'outcome'"
    class="md"
    v-html="md(order.objective)"
  ></div>
  <div
    v-else-if="section === 'brief'"
    class="md"
    v-html="md(text ?? order.brief)"
  ></div>
  <div v-else-if="section === 'spec'" class="md" v-html="md(order.spec)"></div>
  <template v-else-if="section === 'acceptance'">
    <ul v-if="order.acceptance" class="check-list">
      <li
        v-for="(criterion, i) in order.acceptance
          .split(/\n|\.\s+/)
          .filter(Boolean)"
        :key="i"
      >
        <CircleCheck class="icon" /><span>{{ criterion }}</span>
      </li>
    </ul>
    <p v-else>No acceptance criteria recorded.</p>
  </template>
  <template v-else-if="section === 'tasks'">
    <div v-if="tasks.length" class="stack" style="gap: 14px">
      <div
        v-for="(t, i) in tasks"
        :key="t.id"
        class="row"
        style="align-items: start"
      >
        <span
          class="square"
          style="width: 25px; height: 25px; font-size: 11px; flex-shrink: 0"
          >{{ i + 1 }}</span
        >
        <div style="min-width: 0">
          <strong class="small">{{ t.title }}</strong>
          <p class="help">{{ t.objective }}</p>
          <p v-if="t.scope?.length" class="help">
            Scope: {{ t.scope.join(", ") }}
          </p>
          <p v-if="t.deps?.length" class="help">
            After:
            {{
              t.deps
                .map((d) => tasks.find((n) => n.id === d)?.title ?? d)
                .join(", ")
            }}
          </p>
        </div>
      </div>
    </div>
    <p v-else>
      {{
        order.state === "CREATED"
          ? "Draft a blueprint to see the task breakdown."
          : "The task plan will appear when planning is complete."
      }}
    </p>
  </template>
  <p v-else>
    Work follows the recorded outcome and acceptance contract. New ambiguity
    returns to the human. Verification evidence identifies its revision.
  </p>
</template>
<style scoped>
.md {
  display: grid;
  gap: 8px;
  line-height: 1.6;
  min-width: 0;
}
.md :deep(:where(p, ul, ol, h4, pre)) {
  margin: 0;
}
.md :deep(h4) {
  margin-top: 6px;
  font: 600 11px/1.2 var(--font-mono);
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--muted);
}
.md :deep(h4:first-child) {
  margin-top: 0;
}
.md :deep(ul),
.md :deep(ol) {
  padding-left: 18px;
  display: grid;
  gap: 4px;
}
.md :deep(ul) {
  list-style: disc;
}
.md :deep(ol) {
  list-style: decimal;
}
.md :deep(li::marker) {
  color: var(--muted);
}
.md :deep(strong) {
  font-weight: 600;
}
.md :deep(code) {
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  background: var(--surface-2);
}
.md :deep(pre) {
  overflow: auto;
  padding: 10px 12px;
  border-radius: var(--radius-md);
  background: var(--surface-2);
}
.md :deep(pre code) {
  padding: 0;
  background: none;
}
.md :deep(a) {
  color: var(--accent);
  text-decoration: underline;
}
</style>
