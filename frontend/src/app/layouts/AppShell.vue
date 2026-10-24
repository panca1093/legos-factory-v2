<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { RouterLink, RouterView, useRoute, useRouter } from "vue-router";
import {
  LayoutGrid,
  Factory,
  BookOpen,
  Settings,
  Search,
  Moon,
  Sun,
  Bell,
  Menu,
  ChevronRight,
  ChevronDown,
  LogOut,
} from "lucide-vue-next";
import { useSession } from "../../stores/session";
import { useFactory } from "../../stores/factory";
import AppDialog from "../../components/ui/AppDialog.vue";
import StatusBadge from "../../components/ui/StatusBadge.vue";
import {
  attentionStates,
  isActive,
  money,
  projectColor,
} from "../../composables/presentation";
import { useTheme } from "../../composables/useTheme";
const session = useSession(),
  factory = useFactory(),
  route = useRoute(),
  router = useRouter();
const { theme, toggleTheme } = useTheme();
const mobileOpen = ref(false),
  dialog = ref<"search" | "notifications" | null>(null),
  query = ref("");
const error = ref("");
const nav = [
  { to: "/", label: "Overview", icon: LayoutGrid },
  { to: "/factory", label: "Factory", icon: Factory },
  { to: "/knowledge", label: "Knowledge", icon: BookOpen },
  { to: "/settings", label: "Settings", icon: Settings },
];
const current = computed(
  () =>
    nav.find((n) =>
      n.to === "/" ? route.path === "/" : route.path.startsWith(n.to),
    )?.label ?? "Workspace",
);
const initials = computed(
  () =>
    session.user?.name
      .split(" ")
      .slice(0, 2)
      .map((n) => n[0])
      .join("") ?? "?",
);
const budget = computed(() => factory.overview?.budget);
const budgetPercent = computed(() =>
  budget.value?.limit_micros
    ? Math.min(
        100,
        Math.round(
          (budget.value.spent_micros / budget.value.limit_micros) * 100,
        ),
      )
    : 0,
);
const attention = computed(() =>
  factory.workOrders.filter((w) => attentionStates.includes(w.state)),
);
const results = computed(() =>
  factory.workOrders.filter((w) =>
    (
      w.title +
      " " +
      w.id +
      " " +
      factory.projects.find((p) => p.id === w.project_id)?.name
    )
      .toLowerCase()
      .includes(query.value.toLowerCase()),
  ),
);
const projects = computed(() =>
  factory.projects.filter((p) =>
    (p.name + " " + p.repo_url)
      .toLowerCase()
      .includes(query.value.toLowerCase()),
  ),
);
function shortcut(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
    e.preventDefault();
    query.value = "";
    dialog.value = "search";
  }
  if (e.key === "Escape") mobileOpen.value = false;
}
watch(
  () => route.fullPath,
  () => {
    mobileOpen.value = false;
    dialog.value = null;
  },
);
onMounted(async () => {
  document.addEventListener("keydown", shortcut);
  try {
    await Promise.all([
      factory.refreshProjects(),
      factory.refreshWorkOrders(),
      factory.refreshOverview(),
    ]);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
onBeforeUnmount(() => document.removeEventListener("keydown", shortcut));
async function logout() {
  try {
    await session.logout();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}
</script>
<template>
  <a class="skip" href="#main">Skip to content</a>
  <div
    class="mobile-scrim"
    :class="{ show: mobileOpen }"
    @click="mobileOpen = false"
  ></div>
  <aside
    class="sidebar"
    :class="{ open: mobileOpen }"
    aria-label="Workspace navigation"
  >
    <RouterLink to="/" class="brand" style="color: var(--ink)"
      ><div class="brand-mark" aria-hidden="true">
        <i></i><i></i><i></i><i></i>
      </div>
      <span class="brand-name"
        >legos-factory<span style="color: var(--violet)">.</span></span
      ></RouterLink
    >
    <RouterLink to="/settings?tab=team" class="workspace"
      ><span class="square">{{ initials.slice(0, 1) }}</span
      ><span class="grow" style="text-align: left; color: var(--ink)"
        ><strong class="small"
          >{{ session.user?.name.split(" ")[0] }}’s workspace</strong
        ><span class="tiny muted" style="display: block"
          >Team workspace</span
        ></span
      ><ChevronDown class="icon"
    /></RouterLink>
    <nav class="nav" aria-label="Primary">
      <RouterLink
        v-for="n in nav"
        :key="n.to"
        :to="n.to"
        :aria-label="n.label"
        :class="{ active: current === n.label }"
        ><component :is="n.icon" class="icon" /><span>{{ n.label }}</span
        ><b v-if="n.label === 'Factory'" class="count">{{
          factory.workOrders.filter(isActive).length
        }}</b
        ><b v-if="n.label === 'Knowledge'" class="count">β</b></RouterLink
      >
    </nav>
    <div class="nav-label">Your projects</div>
    <div class="sidebar-projects">
      <RouterLink
        v-for="(p, i) in factory.projects"
        :key="p.id"
        class="project-nav"
        :to="{ path: '/factory', query: { project: p.id } }"
        ><i class="project-dot" :style="{ '--c': projectColor(i) }"></i
        >{{ p.name }}</RouterLink
      >
      <p
        v-if="!factory.projects.length"
        class="tiny muted"
        style="padding: 8px 13px"
      >
        Projects appear here.
      </p>
    </div>
    <div class="side-bottom">
      <RouterLink
        to="/settings?tab=usage"
        class="usage-mini"
        style="display: block; color: var(--ink)"
        ><div class="row between small">
          <span>Workspace budget</span
          ><strong>{{
            budget?.limit_micros ? budgetPercent + "%" : "No cap"
          }}</strong>
        </div>
        <div class="track" style="margin: 9px 0 6px">
          <span :style="{ width: budgetPercent + '%' }"></span>
        </div>
        <div class="tiny muted">
          {{ money(budget?.spent_micros)
          }}{{
            budget?.limit_micros
              ? " of " + money(budget.limit_micros)
              : " spent"
          }}
        </div></RouterLink
      >
      <RouterLink
        class="profile"
        to="/settings?tab=preferences"
        style="color: var(--ink)"
        ><span class="avatar">{{ initials }}</span
        ><span class="grow"
          ><strong class="small">{{ session.user?.name }}</strong
          ><span class="tiny muted" style="display: block"
            >Workspace {{ session.user?.role.toLowerCase() }}</span
          ></span
        ><ChevronDown class="icon"
      /></RouterLink>
    </div>
  </aside>
  <div class="shell">
    <header class="topbar">
      <div class="row">
        <button
          class="icon-btn mobile-menu"
          aria-label="Toggle navigation"
          :aria-expanded="mobileOpen"
          @click="mobileOpen = !mobileOpen"
        >
          <Menu class="icon" />
        </button>
        <div class="breadcrumb">
          <span>Workspace</span><ChevronRight class="icon" /><strong>{{
            current
          }}</strong
          ><span v-if="route.params.id" class="tiny"
            >/ {{ String(route.params.id).slice(0, 12) }}</span
          >
        </div>
      </div>
      <div class="top-actions">
        <span class="demo-tag"
          ><i class="dot"></i
          >{{ session.mode === "dev" ? "Local workspace" : "Workspace" }}</span
        ><button
          class="search-trigger"
          aria-label="Search work orders"
          @click="
            dialog = 'search';
            query = '';
          "
        >
          <Search class="icon" /><span>Find work, projects…</span
          ><kbd>⌘ K</kbd></button
        ><button
          class="icon-btn"
          aria-label="Switch theme"
          @click="toggleTheme"
        >
          <Moon v-if="theme === 'light'" class="icon" /><Sun
            v-else
            class="icon"
          /></button
        ><button
          class="icon-btn"
          aria-label="Open notifications"
          @click="dialog = 'notifications'"
        >
          <Bell class="icon" />
        </button>
      </div>
    </header>
    <main id="main" class="main" tabindex="-1">
      <div v-if="error" class="error-state" role="alert">
        {{ error
        }}<button class="btn small" @click="router.go(0)">
          Reload workspace
        </button>
      </div>
      <RouterView v-slot="{ Component }"
        ><Transition name="view" mode="out-in"
          ><component :is="Component" /></Transition
      ></RouterView>
    </main>
    <footer class="footer">
      <span>Made of ideas. Assembled with care.</span
      ><button class="tiny muted" @click="logout">
        <LogOut class="icon" style="width: 12px; height: 12px" /> Sign out</button
      ><span
        >Evidence from your workspace · execution harness labeled per run</span
      >
    </footer>
  </div>
  <AppDialog
    :open="dialog === 'search'"
    title="Find your next piece"
    subtitle="Search work orders and projects"
    @close="dialog = null"
    ><div class="input-wrap" style="margin-bottom: 17px">
      <Search class="icon" /><input
        v-model="query"
        aria-label="Search projects and work orders"
        placeholder="Search work orders or projects…"
      />
    </div>
    <div class="search-results">
      <RouterLink
        v-for="p in projects"
        :key="p.id"
        class="search-result"
        :to="{ path: '/factory', query: { project: p.id } }"
        ><strong>{{ p.name }}</strong
        ><small>Project · {{ p.repo_url }}</small></RouterLink
      ><RouterLink
        v-for="w in results"
        :key="w.id"
        class="search-result"
        :to="`/factory/${w.id}`"
        ><strong>{{ w.title }}</strong
        ><small
          >{{ w.id }} ·
          {{ factory.projects.find((p) => p.id === w.project_id)?.name }}</small
        ></RouterLink
      >
      <div v-if="!projects.length && !results.length" class="empty">
        No matching projects or work orders.
      </div>
    </div></AppDialog
  >
  <AppDialog
    :open="dialog === 'notifications'"
    title="A few things need your eye"
    @close="dialog = null"
    ><div class="search-results">
      <RouterLink
        v-for="w in attention"
        :key="w.id"
        class="search-result"
        :to="`/factory/${w.id}`"
        ><StatusBadge :state="w.state" /><strong
          style="display: block; margin-top: 8px"
          >{{ w.title }}</strong
        ><small>{{ w.id }}</small></RouterLink
      >
      <div v-if="!attention.length" class="empty">No decisions waiting.</div>
    </div></AppDialog
  >
</template>
