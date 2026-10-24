<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useFactory } from '../../stores/factory'
import { Blocks } from 'lucide-vue-next'

const factory = useFactory()
const enabled = ref(false)

onMounted(() => {
  factory.refreshWorkOrders().catch(() => {})
})
</script>

<template>
  <div class="mx-auto max-w-3xl">
    <h1 class="text-2xl font-extrabold">Knowledge</h1>
    <p class="mt-1 text-sm" style="color: var(--muted)">Cataloger — experimental preview. The full second brain is future scope.</p>

    <div class="panel mt-6 p-6 text-center">
      <div class="mx-auto grid h-16 w-16 place-items-center rounded-2xl" style="background: var(--violet-soft)">
        <Blocks :size="28" style="color: var(--violet)" />
      </div>
      <h2 class="mt-3 text-lg font-extrabold">Cataloger is not implemented yet</h2>
      <p class="mx-auto mt-2 max-w-md text-sm" style="color: var(--muted)">
        The MVP exposes only the extension point. No ingestion, retrieval or automatic
        promotion of any data happens in this build. When enabled later, it would curate
        source-linked decisions, patterns and change records for human review.
      </p>
      <button class="btn subtle mt-4" @click="enabled = !enabled">
        {{ enabled ? 'Preview enabled (no data collected)' : 'Enable sample preview (no data collected)' }}
      </button>
      <div v-if="enabled" class="mt-4 text-xs" style="color: var(--muted)">
        {{ factory.workOrders.length }} work order(s) exist as future provenance sources. Nothing is ingested.
      </div>
    </div>
  </div>
</template>
