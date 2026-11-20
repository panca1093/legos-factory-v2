<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import { useRoute } from "vue-router";
import { Lock, TriangleAlert } from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import type { RuntimeInfo, Settings } from "../../types/api";
import "./settings.css";
import AdvancedSection from "./AdvancedSection.vue";
import AgentsSection from "./AgentsSection.vue";
import ConnectionsSection from "./ConnectionsSection.vue";

const factory = useFactory(),
  session = useSession(),
  route = useRoute();
const config = ref<Settings>({
  architect_model: "",
  builder_model: "",
  inspector_model: "",
  allowed_models: null,
});
const runtime = ref<RuntimeInfo | null>(null);
const loading = ref(true),
  initialized = ref(false),
  error = ref("");

const sections = [
  { id: "agents", label: "Agents" },
  { id: "connections", label: "Connections" },
  { id: "advanced", label: "Advanced" },
];
const legacyTabs: Record<string, string> = {
  governance: "agents",
  integrations: "connections",
  usage: "agents",
};
const target = computed(() => {
  const hash = route.hash.replace(/^#/, "");
  return hash || legacyTabs[String(route.query.tab ?? "")] || "";
});
const current = ref("agents");
// A chosen section stays highlighted while the jump to it settles, even when it cannot reach the top.
let pinnedUntil = 0;
function jumpTo(id: string) {
  current.value = id;
  pinnedUntil = performance.now() + 600;
  document.getElementById(id)?.scrollIntoView?.({ block: "start" });
}
async function scrollToTarget() {
  await nextTick();
  if (target.value) jumpTo(target.value);
}
function select(id: string) {
  if (id === target.value) jumpTo(id);
  else current.value = id;
}

// The current section is the last one whose top passed a third of the viewport; at the bottom, the last section.
let frame = 0;
function spy() {
  frame = 0;
  if (performance.now() < pinnedUntil) return;
  const line = window.innerHeight / 3;
  const atBottom =
    window.innerHeight + window.scrollY >=
    document.documentElement.scrollHeight - 2;
  let id = sections[0].id;
  for (const s of sections) {
    const el = document.getElementById(s.id);
    if (el && (atBottom || el.getBoundingClientRect().top <= line)) id = s.id;
  }
  current.value = id;
}
function onScroll() {
  frame ||= requestAnimationFrame(spy);
}
onMounted(() => {
  window.addEventListener("scroll", onScroll, { passive: true });
  window.addEventListener("resize", onScroll, { passive: true });
});
onBeforeUnmount(() => {
  window.removeEventListener("scroll", onScroll);
  window.removeEventListener("resize", onScroll);
  if (frame) cancelAnimationFrame(frame);
});

// On narrow screens the nav scrolls sideways; keep the current item in view.
const navEl = ref<HTMLElement>();
watch(current, async () => {
  await nextTick();
  const nav = navEl.value;
  const a = nav?.querySelector<HTMLElement>('[aria-current="true"]');
  if (!nav || !a || nav.scrollWidth <= nav.clientWidth) return;
  const off = a.getBoundingClientRect().left - nav.getBoundingClientRect().left;
  if (off < 0 || off + a.offsetWidth > nav.clientWidth)
    nav.scrollLeft += off - 8;
});

async function load() {
  loading.value = true;
  error.value = "";
  const [settings, rt] = await Promise.allSettled([
    factory.loadSettings(),
    factory.loadRuntime(),
  ]);
  initialized.value = settings.status === "fulfilled";
  if (settings.status === "fulfilled") config.value = settings.value;
  if (rt.status === "fulfilled") runtime.value = rt.value;
  error.value = [settings, rt]
    .filter((r) => r.status === "rejected")
    .map((r) =>
      r.reason instanceof Error ? r.reason.message : String(r.reason),
    )
    .join(" · ");
  loading.value = false;
  await scrollToTarget();
  if (!target.value) spy();
}
onMounted(load);
watch(target, scrollToTarget);

function merge(patch: Partial<Settings>) {
  for (const [k, v] of Object.entries(patch))
    if (v !== undefined)
      (config.value as unknown as Record<string, unknown>)[k] = v;
}
const credentialsOK = computed(
  () => runtime.value?.credentials_configured ?? true,
);
</script>
<template>
  <div class="settings-layout">
    <aside class="settings-side">
      <h1>Settings</h1>
      <nav ref="navEl" class="settings-nav" aria-label="Settings sections">
        <RouterLink
          v-for="s in sections"
          :key="s.id"
          :to="{ hash: `#${s.id}` }"
          :aria-current="current === s.id ? 'true' : undefined"
          @click="select(s.id)"
          >{{ s.label }}</RouterLink
        >
      </nav>
      <p class="settings-who">
        Signed in as
        <span class="pill purple">{{ session.user?.role }}</span>
      </p>
    </aside>
    <div class="detail-content settings-main">
      <div v-if="loading" class="panel empty" aria-busy="true">
        Loading workspace settings…
      </div>
      <template v-else>
        <div v-if="error" class="error-state" role="alert">
          {{ error }}<button class="btn small" @click="load">Try again</button>
        </div>
        <p
          v-if="runtime && !credentialsOK"
          class="notice"
          data-test="secret-banner"
        >
          <TriangleAlert class="icon" aria-hidden="true" />
          <span
            ><b>Credentials can't be saved.</b> Set
            <code>FACTORY_SECRET_KEY</code> on the server and restart it. Until
            then you can't add accounts or connections.</span
          >
        </p>
        <p v-if="!session.isAdmin" class="notice" data-test="read-only-note">
          <Lock class="icon" aria-hidden="true" /> You can look but not change
          workspace defaults. Ask an admin to change them.
        </p>

        <AgentsSection
          :settings="config"
          :editable="session.isAdmin && initialized"
          :can-write="session.canWrite"
          :credentials-configured="credentialsOK"
          @saved="merge"
        />
        <ConnectionsSection :credentials-configured="credentialsOK" />
        <AdvancedSection
          :settings="config"
          :runtime="runtime"
          :editable="session.isAdmin && initialized"
          @saved="merge"
        />
      </template>
    </div>
  </div>
</template>
