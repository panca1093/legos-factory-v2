<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
const open = defineModel<boolean>("open", { default: false });
const root = ref<HTMLElement>();
function outside(e: MouseEvent) {
  if (open.value && !root.value?.contains(e.target as Node)) open.value = false;
}
function escape(e: KeyboardEvent) {
  if (open.value && e.key === "Escape") open.value = false;
}
onMounted(() => {
  document.addEventListener("mousedown", outside);
  document.addEventListener("keydown", escape);
});
onBeforeUnmount(() => {
  document.removeEventListener("mousedown", outside);
  document.removeEventListener("keydown", escape);
});
</script>
<template>
  <div ref="root" class="lg-pop">
    <slot name="trigger" :toggle="() => (open = !open)" :open="open" />
    <div v-if="open" class="lg-menu" role="menu" @click="open = false">
      <slot />
    </div>
  </div>
</template>
