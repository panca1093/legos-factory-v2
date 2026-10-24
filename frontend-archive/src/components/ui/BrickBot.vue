<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    persona: 'foreman' | 'architect' | 'builder' | 'inspector' | 'cataloger'
    size?: number
  }>(),
  { size: 64 },
)

const palette = computed(() => {
  const p: Record<string, { body: string; head: string; accent: string }> = {
    foreman: { body: '#ffb74d', head: '#f7c65e', accent: '#e8a020' },
    architect: { body: '#a499ff', head: '#c9c1ff', accent: '#8a7bf0' },
    builder: { body: '#7fb2ff', head: '#a9c9ff', accent: '#5b8ef0' },
    inspector: { body: '#a8dcca', head: '#c4ecdb', accent: '#5aa887' },
    cataloger: { body: '#ff96a7', head: '#ffbcc7', accent: '#e8758a' },
  }
  return p[props.persona] ?? p.foreman
})
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 64 64"
    role="img"
    :aria-label="`Brick character: ${persona}`"
  >
    <!-- head brick -->
    <rect x="14" y="14" width="36" height="26" rx="6" :fill="palette.head" />
    <!-- studs -->
    <rect x="20" y="8" width="8" height="8" rx="2.5" :fill="palette.head" />
    <rect x="36" y="8" width="8" height="8" rx="2.5" :fill="palette.head" />
    <!-- eyes -->
    <circle cx="25" cy="27" r="3" :fill="palette.accent" />
    <circle cx="39" cy="27" r="3" :fill="palette.accent" />
    <!-- smile -->
    <path d="M25 33 Q32 37 39 33" fill="none" :stroke="palette.accent" stroke-width="2" stroke-linecap="round" />
    <!-- role-specific accessory -->
    <template v-if="persona === 'foreman'">
      <rect x="10" y="10" width="44" height="10" rx="5" fill="#e8a020" />
      <rect x="26" y="4" width="12" height="8" rx="3" fill="#e8a020" />
    </template>
    <template v-else-if="persona === 'architect'">
      <ellipse cx="32" cy="16" rx="12" ry="5" fill="#7a6be8" />
      <rect x="42" y="12" width="12" height="3" rx="1.5" fill="#7a6be8" transform="rotate(-14 42 14)" />
    </template>
    <template v-else-if="persona === 'builder'">
      <path d="M12 20 a20 12 0 0 1 40 0 z" fill="#4f7fe0" />
      <rect x="12" y="19" width="40" height="5" rx="2.5" fill="#3d6bd0" />
    </template>
    <template v-else-if="persona === 'inspector'">
      <circle cx="25" cy="27" r="7" fill="none" stroke="#4f8f70" stroke-width="2.5" />
      <circle cx="39" cy="27" r="7" fill="none" stroke="#4f8f70" stroke-width="2.5" />
      <line x1="32" y1="27" x2="32" y2="27" stroke="#4f8f70" stroke-width="2.5" />
    </template>
    <template v-else>
      <rect x="44" y="30" width="10" height="14" rx="2" fill="#e8758a" />
      <line x1="49" y1="30" x2="49" y2="44" stroke="#fff" stroke-width="1.5" />
    </template>
    <!-- torso brick -->
    <rect x="18" y="42" width="28" height="16" rx="5" :fill="palette.body" />
    <rect x="24" y="38" width="6" height="6" rx="2" :fill="palette.body" />
    <rect x="34" y="38" width="6" height="6" rx="2" :fill="palette.body" />
  </svg>
</template>
