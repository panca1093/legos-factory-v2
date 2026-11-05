<script setup lang="ts">
import { Search } from "lucide-vue-next";
const model = defineModel<string>({ default: "" });
defineProps<{ label: string; placeholder?: string }>();
</script>
<template>
  <div class="lg-search">
    <Search class="icon" />
    <input
      v-model="model"
      class="lg-field"
      type="search"
      :aria-label="label"
      :placeholder="placeholder"
    />
  </div>
</template>
