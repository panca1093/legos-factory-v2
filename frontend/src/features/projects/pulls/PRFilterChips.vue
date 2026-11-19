<script setup lang="ts">
import type { PRFilter } from "../../../types/review";

defineProps<{ modelValue: PRFilter }>();
defineEmits<{ "update:modelValue": [PRFilter] }>();
const chips: { key: PRFilter; label: string }[] = [
  { key: "related", label: "Related to me" },
  { key: "reviewer", label: "Reviewer: me" },
  { key: "author", label: "Author: me" },
];
</script>
<template>
  <div class="fx-chips" role="group" aria-label="Filter pull requests">
    <button
      v-for="c in chips"
      :key="c.key"
      type="button"
      class="fx-chip"
      :class="{ on: modelValue === c.key }"
      :aria-pressed="modelValue === c.key"
      :data-test="`chip-${c.key}`"
      @click="$emit('update:modelValue', c.key)"
    >
      {{ c.label }}
    </button>
  </div>
</template>
