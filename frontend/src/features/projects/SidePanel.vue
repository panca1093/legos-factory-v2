<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";

export type SideTab = {
  key: string;
  label: string;
  badge?: string | number;
  hot?: boolean;
};
const props = defineProps<{
  tabs: SideTab[];
  label: string;
  initial?: string;
}>();
const route = useRoute(),
  router = useRouter();
// ?side= so it never clashes with a page's own ?tab=.
const tab = computed(() => {
  const q = route.query.side;
  const keys = props.tabs.map((t) => t.key);
  if (typeof q === "string" && keys.includes(q)) return q;
  return props.initial && keys.includes(props.initial)
    ? props.initial
    : keys[0];
});
function select(key: string) {
  void router.replace({ query: { ...route.query, side: key } });
}
function step(d: number) {
  const i = props.tabs.findIndex((t) => t.key === tab.value);
  const next = props.tabs[(i + d + props.tabs.length) % props.tabs.length];
  select(next.key);
  document.getElementById(`sp-tab-${next.key}`)?.focus();
}
</script>
<template>
  <section class="sp" data-test="side-panel">
    <div
      class="sp-tabs"
      role="tablist"
      :aria-label="label"
      @keydown.left.prevent="step(-1)"
      @keydown.right.prevent="step(1)"
    >
      <button
        v-for="t in tabs"
        :id="`sp-tab-${t.key}`"
        :key="t.key"
        type="button"
        role="tab"
        :aria-selected="tab === t.key"
        :tabindex="tab === t.key ? 0 : -1"
        :aria-controls="`sp-${t.key}`"
        :data-test="`tab-${t.key}`"
        @click="select(t.key)"
      >
        {{ t.label }}
        <span
          v-if="t.badge !== undefined && t.badge !== ''"
          class="sp-badge"
          :class="{ hot: t.hot }"
          :data-test="`tab-${t.key}-badge`"
          >{{ t.badge }}</span
        >
      </button>
    </div>
    <div
      v-for="t in tabs"
      v-show="tab === t.key"
      :id="`sp-${t.key}`"
      :key="t.key"
      class="sp-body"
      role="tabpanel"
      :aria-labelledby="`sp-tab-${t.key}`"
    >
      <slot :name="t.key" />
    </div>
  </section>
</template>
