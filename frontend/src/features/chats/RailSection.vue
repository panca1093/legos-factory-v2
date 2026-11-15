<script setup lang="ts">
import { ChevronDown } from "lucide-vue-next";
defineProps<{
  id: string;
  title: string;
  summary?: string;
  tone?: "ok" | "muted";
  open: boolean;
}>();
const emit = defineEmits<{ toggle: [] }>();
</script>
<template>
  <section class="ctx-sec">
    <h4>
      <button
        :id="`rail-${id}-h`"
        type="button"
        class="rail-h"
        :aria-expanded="open"
        :aria-controls="`rail-${id}`"
        @click="emit('toggle')"
      >
        <span class="rail-t">{{ title }}</span>
        <span
          v-if="summary"
          class="rail-sum"
          :class="tone && ['rail-pill', tone]"
          >{{ summary }}</span
        >
        <ChevronDown
          class="icon rail-chev"
          :data-open="open"
          aria-hidden="true"
        />
      </button>
    </h4>
    <div
      v-show="open"
      :id="`rail-${id}`"
      class="rail-body"
      role="region"
      :aria-labelledby="`rail-${id}-h`"
    >
      <slot />
    </div>
  </section>
</template>
