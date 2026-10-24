<script setup lang="ts">
import { RouterLink, RouterView } from 'vue-router'
import { useSession } from '../../stores/session'
import { ref } from 'vue'
import {
  Boxes, BookOpen, Factory, Home, LogOut, Menu, Settings, X,
} from 'lucide-vue-next'

const session = useSession()
const mobileOpen = ref(false)

const nav = [
  { to: '/', label: 'Overview', icon: Home },
  { to: '/factory', label: 'Factory', icon: Factory },
  { to: '/knowledge', label: 'Knowledge', icon: BookOpen },
  { to: '/settings', label: 'Settings', icon: Settings },
]

function go() {
  mobileOpen.value = false
}
</script>

<template>
  <div class="min-h-screen flex" style="background: var(--bg)">
    <!-- skip link -->
    <a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:m-2 btn subtle">Skip to content</a>

    <!-- mobile scrim -->
    <div v-if="mobileOpen" class="fixed inset-0 z-30 bg-black/40 lg:hidden" @click="mobileOpen = false"></div>

    <!-- rail -->
    <aside
      :class="['fixed inset-y-0 left-0 z-40 w-60 flex flex-col p-4 border-r transition-transform lg:static lg:translate-x-0',
        mobileOpen ? 'translate-x-0' : '-translate-x-full']"
      style="border-color: var(--line); background: var(--panel)"
    >
      <div class="flex items-center gap-2 px-2 mb-6">
        <div class="grid h-9 w-9 place-items-center rounded-xl" style="background: var(--violet)">
          <Boxes :size="18" color="#fff" />
        </div>
        <div>
          <div class="font-extrabold tracking-tight">legos-factory</div>
          <div class="text-[10px] font-bold uppercase tracking-widest" style="color: var(--muted)">build crew</div>
        </div>
        <button class="ml-auto lg:hidden" aria-label="Close navigation" @click="mobileOpen = false">
          <X :size="18" />
        </button>
      </div>

      <nav class="flex flex-col gap-1" aria-label="Primary">
        <RouterLink
          v-for="item in nav"
          :key="item.to"
          :to="item.to"
          class="flex items-center gap-3 rounded-xl px-3 py-2.5 font-semibold no-underline"
          style="color: var(--ink)"
          active-class="rail-active"
          @click="go"
        >
          <component :is="item.icon" :size="17" />
          {{ item.label }}
        </RouterLink>
      </nav>

      <div class="mt-auto flex items-center gap-2 rounded-xl p-3" style="background: var(--panel2)">
        <div class="grid h-8 w-8 place-items-center rounded-full text-xs font-extrabold" style="background: var(--violet-soft); color: var(--violet)">
          {{ (session.user?.name ?? '?').slice(0, 2).toUpperCase() }}
        </div>
        <div class="min-w-0 flex-1">
          <div class="truncate text-xs font-bold">{{ session.user?.name ?? 'Not signed in' }}</div>
          <div class="text-[10px] uppercase tracking-wider" style="color: var(--muted)">{{ session.user?.role ?? '—' }}</div>
        </div>
        <button class="btn small text" aria-label="Sign out" @click="session.logout()"><LogOut :size="14" /></button>
      </div>
    </aside>

    <!-- main -->
    <div class="flex min-h-screen min-w-0 flex-1 flex-col">
      <header class="sticky top-0 z-20 flex items-center gap-3 border-b px-4 py-3 lg:hidden" style="border-color: var(--line); background: var(--panel)">
        <button class="icon-btn" aria-label="Open navigation" @click="mobileOpen = true"><Menu :size="20" /></button>
        <span class="font-extrabold">legos-factory</span>
      </header>

      <main id="main" class="flex-1 p-4 lg:p-7">
        <RouterView />
      </main>
    </div>
  </div>
</template>

<style scoped>
.rail-active {
  background: var(--violet-soft);
  color: var(--violet) !important;
}
</style>
