<script setup lang="ts">
import { computed } from "vue";
import { stateInfo } from "../../composables/presentation";
const props = defineProps<{ state: string }>();
const info = computed(() => stateInfo(props.state));
const legacy = computed(
  () =>
    ({ green: "mint", red: "red", orange: "amber", purple: "violet" })[
      info.value.tone
    ],
);
</script>
<template>
  <span class="pill" :class="[info.tone, legacy]"
    ><i class="dot"></i>{{ info.label }}</span
  >
</template>
