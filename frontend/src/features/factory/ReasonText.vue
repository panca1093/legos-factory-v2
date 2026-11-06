<script setup lang="ts">
import { computed, ref } from "vue";
const props = defineProps<{
  text: string;
  label?: string;
  suffix?: string;
  items?: string[];
}>();
const open = ref(false);
const parts = computed(() =>
  props.items?.length
    ? props.items
    : props.text
        .split(";")
        .map((p) => p.trim())
        .filter(Boolean),
);
const list = computed(() => parts.value.length > 1);
const long = computed(() => list.value || props.text.length > 180);
</script>
<template>
  <div class="fx-reason">
    <strong v-if="label">{{ label }}</strong>
    <ul v-if="open && list" class="fx-reason-list">
      <li v-for="(p, i) in parts" :key="i">{{ p }}</li>
    </ul>
    <span v-else :class="{ clamp: long && !open }" data-test="reason-text">
      {{ text }}
    </span>
    <template v-if="suffix"> {{ suffix }}</template>
    <button
      v-if="long"
      type="button"
      class="fx-link fx-reason-toggle"
      :aria-expanded="open"
      @click="open = !open"
    >
      {{ open ? "Show less" : "Show more" }}
    </button>
  </div>
</template>
