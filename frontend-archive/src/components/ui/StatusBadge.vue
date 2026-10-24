<script setup lang="ts">
import { computed } from 'vue'
import type { WorkOrderState } from '../../types/api'

const props = defineProps<{ state: WorkOrderState }>()

const kind = computed(() => {
  switch (props.state) {
    case 'READY': return 'mint'
    case 'NEEDS_HUMAN_REVIEW': return 'amber'
    case 'BLOCKED': return 'red'
    case 'CANCELLED': return 'red'
    case 'IMPLEMENTING': return 'violet'
    case 'VERIFYING': case 'SPECIALIST_REVIEW': return 'violet'
    case 'WAITING_FOR_HUMAN': return 'amber'
    default: return ''
  }
})

const label = computed(() => props.state.replaceAll('_', ' ').toLowerCase())
</script>

<template>
  <span class="badge" :class="kind">
    <span
      v-if="['IMPLEMENTING','VERIFYING','SPECIALIST_REVIEW','QUEUED'].includes(state)"
      class="inline-block h-1.5 w-1.5 rounded-full bg-current"
      style="animation: pulse 1.4s infinite"
    ></span>
    {{ label }}
  </span>
</template>

<style scoped>
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
</style>
