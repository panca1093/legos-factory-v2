<script setup lang="ts">
defineProps<{ label: string; badge?: boolean }>();
</script>
<template>
  <button class="lg-icon-btn" type="button" :aria-label="label" :title="label">
    <slot /><i v-if="badge" class="badge"></i>
  </button>
</template>
