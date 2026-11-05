<script setup lang="ts">
defineProps<{
  variant?: "primary" | "ghost" | "danger";
  small?: boolean;
  pill?: boolean;
}>();
</script>
<template>
  <button class="lg-btn" :class="[variant, { small, pill }]" type="button">
    <slot />
  </button>
</template>
