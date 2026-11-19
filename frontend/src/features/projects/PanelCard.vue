<script setup lang="ts">
import { ref } from "vue";
import { ChevronDown } from "lucide-vue-next";
import { readFlags, writeStored } from "./rail";

const props = defineProps<{ id: string; title: string }>();
const key = "legos-project-panel-cards";
const closed = ref(!!readFlags(key)[props.id]);
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
