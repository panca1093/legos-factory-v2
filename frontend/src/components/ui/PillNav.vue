<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { RouterLink } from "vue-router";
const props = defineProps<{
  items: { to: string; label: string; dot?: boolean }[];
  active: string;
}>();
const root = ref<HTMLElement>();
const thumb = ref({ width: "0px", transform: "translateX(0)" });
function place() {
  const el = root.value?.querySelector<HTMLElement>("a.active");
  thumb.value = {
    width: (el?.offsetWidth ?? 0) + "px",
    transform: `translateX(${el?.offsetLeft ?? 0}px)`,
  };
}
watch(
  () => props.active,
  () => nextTick(place),
);
onMounted(() => {
  place();
  window.addEventListener("resize", place);
  void document.fonts?.ready.then(place);
});
onBeforeUnmount(() => window.removeEventListener("resize", place));
</script>
<template>
  <nav ref="root" class="lg-seg" aria-label="Main">
    <span class="thumb" :style="thumb"></span>
    <RouterLink
      v-for="i in items"
      :key="i.to"
      :to="i.to"
      :class="{ active: active === i.label }"
      :aria-current="active === i.label ? 'page' : undefined"
      >{{ i.label }}<i v-if="i.dot" class="cd"></i
    ></RouterLink>
  </nav>
</template>
