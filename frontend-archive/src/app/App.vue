<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterView } from 'vue-router'
import { useSession } from '../stores/session'
import { subscribe } from '../services/events/sse'
import { useFactory } from '../stores/factory'

const session = useSession()
const factory = useFactory()

onMounted(async () => {
  await session.bootstrap()
  if (session.user) {
    // global live feed: keep overview + lists fresh
    subscribe('/api/v1/events', () => {
      factory.refreshOverview().catch(() => {})
      factory.refreshWorkOrders().catch(() => {})
    })
  }
})
</script>

<template>
  <div v-if="session.loading" class="grid min-h-screen place-items-center" style="background: var(--bg)">
    <main class="panel px-8 py-6 font-bold" aria-busy="true">Starting factory…</main>
  </div>
  <RouterView v-else />
</template>
