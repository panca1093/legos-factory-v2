<script setup lang="ts">
import { ref, watch } from "vue";
import { ChevronDown } from "lucide-vue-next";
import { readFlags, writeStored } from "./rail";

const props = defineProps<{ id: string; title: string; reveal?: boolean }>();
const key = "legos-project-panel-cards";
const closed = ref(!!readFlags(key)[props.id] && !props.reveal);
watch(
  () => props.reveal,
  (r) => {
    if (r) closed.value = false;
  },
);
function toggle() {
  closed.value = !closed.value;
  writeStored(
    key,
    JSON.stringify({ ...readFlags(key), [props.id]: closed.value }),
  );
}
</script>
<template>
  <section class="pc" :data-card="id">
    <h3>
      <button
        type="button"
        class="pc-h"
        :aria-expanded="!closed"
        :aria-controls="`pc-${id}`"
        @click="toggle"
      >
        <span>{{ title }}</span><ChevronDown
          class="icon pc-chev"
          :data-closed="closed"
        />
      </button>
    </h3>
    <div v-show="!closed" :id="`pc-${id}`" class="pc-body"><slot /></div>
  </section>
</template>
