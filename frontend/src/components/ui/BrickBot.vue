<script setup lang="ts">
import BrickBot from "../agents/BrickBot.vue";
defineProps<{
  persona: "foreman" | "architect" | "builder" | "inspector" | "cataloger";
  size?: number;
}>();
</script>
<template><BrickBot :persona="persona" :size="size" /></template>
