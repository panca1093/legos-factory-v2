<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import { RouterLink, RouterView, useRoute, useRouter } from "vue-router";
import {
  Home,
  MessageSquare,
  Factory,
  BookOpen,
  Settings,
  Search,
  Moon,
  Sun,
  Bell,
  PanelLeft,
  PanelLeftClose,
  Plus,
  Gauge,
  LogOut,
} from "lucide-vue-next";
import { useSession } from "../../stores/session";
import { useFactory } from "../../stores/factory";
import { useChats } from "../../stores/chats";
import { chatTitle } from "../../features/chats/chatUtils";
import AppDialog from "../../components/ui/AppDialog.vue";
import StatusBadge from "../../components/ui/StatusBadge.vue";
import Button from "../../components/ui/Button.vue";
import IconButton from "../../components/ui/IconButton.vue";
import PillNav from "../../components/ui/PillNav.vue";
import Popover from "../../components/ui/Popover.vue";
import SearchField from "../../components/ui/SearchField.vue";
import Toast from "../../components/ui/Toast.vue";
import { needsYou, money, projectColor } from "../../composables/presentation";
import { useTheme } from "../../composables/useTheme";
const session = useSession(),
  factory = useFactory(),
  chats = useChats(),
  route = useRoute(),
  router = useRouter();
const { theme, toggleTheme } = useTheme();
const railKey = "legos-factory-rail";
const sideOpen = ref(false),
  menuOpen = ref(false),
  dialog = ref<"search" | "notifications" | null>(null),
  query = ref("");
const error = ref("");
const tabs = [
  { to: "/", label: "Home", icon: Home },
  { to: "/chats", label: "Chats", icon: MessageSquare },
  { to: "/factory", label: "Factory", icon: Factory },
  { to: "/knowledge", label: "Knowledge", icon: BookOpen },
];
const current = computed(
  () =>
    tabs.find((n) =>
      n.to === "/" ? route.path === "/" : route.path.startsWith(n.to),
    )?.label ?? "",
);
const navItems = computed(() =>
  tabs.map((t) => ({
    ...t,
    dot: t.label === "Factory" && attention.value.length > 0,
  })),
);
const toggleBtn = ref<{ $el: HTMLElement }>(),
  closeBtn = ref<{ $el: HTMLElement }>();
const overlay = () => window.innerWidth <= 1000;
async function setSide(open: boolean) {
  sideOpen.value = open;
  if (!overlay()) {
    try {
      localStorage.setItem(railKey, open ? "1" : "0");
    } catch {
      /* Browser storage can be disabled. */
    }
  }
  await nextTick();
  if (overlay()) (open ? closeBtn : toggleBtn).value?.$el.focus();
}
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
const recentChats = computed(() => chats.chats.slice(0, 5));
const tokens = (t: { tokens_in: number; tokens_out: number }) =>
  new Intl.NumberFormat("en-US", { notation: "compact" }).format(
    t.tokens_in + t.tokens_out,
  );
const attention = computed(() => needsYou(factory.workOrders));
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
  if (e.key === "Escape" && overlay() && sideOpen.value) void setSide(false);
}
watch(
  () => route.fullPath,
  () => {
    if (overlay() && sideOpen.value) void setSide(false);
    dialog.value = null;
  },
);
onMounted(async () => {
  document.addEventListener("keydown", shortcut);
  try {
    sideOpen.value =
      localStorage.getItem(railKey) === "1" && window.innerWidth > 1000;
  } catch {
    sideOpen.value = false;
  }
  try {
    await Promise.all([
      factory.refreshProjects(),
      factory.refreshWorkOrders(),
      factory.refreshOverview(),
      factory.refreshUsage(),
      chats.refresh().catch(() => undefined),
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
  <div class="app" :class="{ 'side-open': sideOpen }">
    <aside id="rail" class="rail" aria-label="Workspace" :inert="!sideOpen">
      <div class="rail-in">
        <div class="rail-head">
          <span class="lg-kind">Workspace</span>
          <IconButton
            ref="closeBtn"
            label="Hide sidebar"
            @click="setSide(false)"
            ><PanelLeftClose class="icon"
          /></IconButton>
        </div>
        <Button
          variant="primary"
          pill
          :disabled="!session.canWrite"
          @click="router.push({ path: '/chats', query: { new: '1' } })"
          ><Plus class="icon" />New chat</Button
        >
        <div class="lg-nav-label">Projects</div>
        <nav class="lg-nav" aria-label="Projects">
          <RouterLink
            v-for="(p, i) in factory.projects"
            :key="p.id"
            :to="{ path: '/factory', query: { project: p.id } }"
            ><i class="dot" :style="{ '--c': projectColor(i) }"></i
            ><span class="t">{{ p.name }}</span></RouterLink
          >
          <p v-if="!factory.projects.length" class="empty-note">
            Projects appear here.
          </p>
        </nav>
        <div class="lg-nav-label">Recent chats</div>
        <nav class="lg-nav grow" aria-label="Recent chats">
          <RouterLink
            v-for="c in recentChats"
            :key="c.id"
            :to="`/chats/${c.id}`"
            active-class="active"
            ><span class="t">{{ chatTitle(c) }}</span></RouterLink
          >
          <p v-if="!recentChats.length" class="empty-note">No chats yet.</p>
        </nav>
        <RouterLink to="/settings?tab=usage" class="usage-card"
          ><div class="top">
            <span>Workspace budget</span><Gauge class="icon" />
          </div>
          <div class="num">
            <b>{{ money(budget?.spent_micros) }}</b>
            {{
              budget?.limit_micros
                ? "of " + money(budget.limit_micros)
                : "spent · no cap"
            }}
          </div>
          <div v-if="budget?.limit_micros" class="bar">
            <i :style="{ width: budgetPercent + '%' }"></i>
          </div>
          <div v-if="factory.usage?.month_to_date" class="num">
            This month
            <b class="mtd">{{
              money(factory.usage.month_to_date.cost_micros)
            }}</b>
            · {{ tokens(factory.usage.month_to_date) }} tokens
          </div></RouterLink
        >
      </div>
    </aside>
    <div class="scrim" @click="setSide(false)"></div>
    <div class="main">
      <header class="topbar">
        <div class="l">
          <IconButton
            ref="toggleBtn"
            :label="sideOpen ? 'Hide sidebar' : 'Show sidebar'"
            aria-controls="rail"
            :aria-expanded="sideOpen"
            @click="setSide(!sideOpen)"
            ><PanelLeft class="icon"
          /></IconButton>
          <RouterLink to="/" class="brand"
            ><span class="mark" aria-hidden="true">L</span
            ><b>Legos</b></RouterLink
          >
        </div>
        <PillNav class="topnav" :items="navItems" :active="current" />
        <div class="r">
          <button
            class="kbtn"
            aria-label="Search work orders"
            @click="
              dialog = 'search';
              query = '';
            "
          >
            <Search class="icon" /><span class="lbl">Search or jump to…</span
            ><kbd>⌘K</kbd>
          </button>
          <IconButton
            label="Open notifications"
            :badge="attention.length > 0"
            @click="dialog = 'notifications'"
            ><Bell class="icon"
          /></IconButton>
          <IconButton label="Switch theme" @click="toggleTheme"
            ><Moon v-if="theme === 'light'" class="icon" /><Sun
              v-else
              class="icon"
          /></IconButton>
          <Popover v-model:open="menuOpen">
            <template #trigger="{ toggle }">
              <button
                class="lg-avatar avatar-btn"
                aria-label="Account menu"
                :aria-expanded="menuOpen"
                @click="toggle"
              >
                {{ initials }}
              </button>
            </template>
            <div class="head">
              <strong>{{ session.user?.name }}</strong
              ><small
                >{{
                  session.mode === "dev" ? "Local workspace" : "Workspace"
                }}
                · {{ session.user?.role.toLowerCase() }}</small
              >
            </div>
            <RouterLink to="/settings"
              ><Settings class="icon" />Settings</RouterLink
            >
            <button @click="logout"><LogOut class="icon" />Sign out</button>
          </Popover>
        </div>
      </header>
      <main id="main" class="view" tabindex="-1">
        <div class="page">
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
        </div>
      </main>
    </div>
  </div>
  <nav class="tabbar" aria-label="Main">
    <RouterLink
      v-for="t in navItems"
      :key="t.to"
      :to="t.to"
      :class="{ active: current === t.label }"
      ><component :is="t.icon" class="icon" />{{ t.label
      }}<i v-if="t.dot" class="cd"></i
    ></RouterLink>
  </nav>
  <Toast />
  <AppDialog
    :open="dialog === 'search'"
    title="Find your next piece"
    subtitle="Search work orders and projects"
    @close="dialog = null"
    ><SearchField
      v-model="query"
      style="margin-bottom: 17px"
      label="Search projects and work orders"
      placeholder="Search work orders or projects…"
    />
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
