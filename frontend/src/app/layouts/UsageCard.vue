<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { RouterLink } from "vue-router";
import { ChevronRight, RefreshCw, UserCog } from "lucide-vue-next";
import {
  api,
  type AgentAccount,
  type AgentInfo,
  type AgentsResponse,
  type UsageWindow,
} from "../../types/api";
import { resetsIn } from "../../composables/presentation";

const agents = ref<AgentInfo[]>([]),
  loadedAt = ref(0),
  now = ref(Date.now()),
  refreshing = ref(false),
  openId = ref(""),
  card = ref<HTMLElement>(),
  panel = ref<HTMLElement>(),
  pos = ref<Record<string, string>>({});
let timer: ReturnType<typeof setInterval> | undefined;

const withUsage = (a: AgentInfo) =>
  a.accounts.filter((x) => x.status === "signed_in" && x.usage);
const rows = computed(() =>
  agents.value
    .filter((a) => a.installed && a.enabled && withUsage(a).length)
    .map((a) => {
      const list = withUsage(a);
      const acc = list.find((x) => x.active) ?? list[0];
      return { agent: a, acc, accounts: list };
    }),
);
const opened = computed(() =>
  rows.value.find((r) => r.agent.id === openId.value),
);
const windows = (acc: AgentAccount) =>
  [
    { key: "5h", name: "Session", w: acc.usage?.session },
    { key: "wk", name: "Weekly", w: acc.usage?.week },
  ].filter((m): m is { key: string; name: string; w: UsageWindow } => !!m.w);
const pct = (w: UsageWindow) => Math.round(w.used_pct);
const tone = (w: UsageWindow) =>
  w.used_pct >= 90 ? "high" : w.used_pct >= 70 ? "warn" : "";
const width = (w: UsageWindow) => `${Math.min(100, Math.max(0, w.used_pct))}%`;
const updated = computed(() => {
  if (!loadedAt.value) return "";
  const m = Math.floor((now.value - loadedAt.value) / 60000);
  return m < 1 ? "Updated just now" : `Updated ${m} min ago`;
});

async function load() {
  try {
    const res = await api.get<AgentsResponse>("/api/v1/agents");
    agents.value = res.agents ?? [];
    loadedAt.value = Date.now();
    now.value = loadedAt.value;
  } catch {
    agents.value = [];
  }
}
async function refresh() {
  if (refreshing.value) return;
  refreshing.value = true;
  try {
    await Promise.all(
      rows.value.map((r) =>
        api
          .post(`/api/v1/agents/${r.agent.id}/accounts/${r.acc.id}/refresh`)
          .catch(() => undefined),
      ),
    );
    await load();
  } finally {
    refreshing.value = false;
  }
}

function place() {
  const r = card.value?.getBoundingClientRect();
  if (!r) return;
  const w = Math.min(340, window.innerWidth - 32);
  pos.value =
    r.right + 12 + w <= window.innerWidth
      ? {
          left: `${r.right + 12}px`,
          bottom: `${window.innerHeight - r.bottom}px`,
        }
      : {
          left: `${Math.max(16, r.left)}px`,
          bottom: `${window.innerHeight - r.top + 8}px`,
        };
  pos.value.width = `${w}px`;
}
async function toggle(id: string) {
  openId.value = openId.value === id ? "" : id;
  now.value = Date.now();
  if (!openId.value) return;
  place();
  await nextTick();
  panel.value?.focus();
}
function outside(e: MouseEvent) {
  const t = e.target as Node;
  if (openId.value && !panel.value?.contains(t) && !card.value?.contains(t))
    openId.value = "";
}
function escape(e: KeyboardEvent) {
  if (openId.value && e.key === "Escape") openId.value = "";
}
const close = () => (openId.value = "");

onMounted(() => {
  void load();
  timer = setInterval(() => void load(), 5 * 60_000);
  document.addEventListener("mousedown", outside);
  document.addEventListener("keydown", escape);
  window.addEventListener("resize", close);
});
onBeforeUnmount(() => {
  clearInterval(timer);
  document.removeEventListener("mousedown", outside);
  document.removeEventListener("keydown", escape);
  window.removeEventListener("resize", close);
});
</script>
<template>
  <div ref="card" class="usage-card">
    <div class="top">
      <span>Usage</span>
      <button
        type="button"
        class="uc-icon"
        aria-label="Refresh usage"
        :disabled="refreshing"
        @click="refresh"
      >
        <RefreshCw class="icon" :class="{ spin: refreshing }" />
      </button>
    </div>
    <button
      v-for="r in rows"
      :key="r.agent.id"
      type="button"
      class="uc-row"
      :class="{ on: openId === r.agent.id }"
      :aria-expanded="openId === r.agent.id"
      :data-agent="r.agent.id"
      @click="toggle(r.agent.id)"
    >
      <span class="uc-ava" aria-hidden="true">{{
        r.agent.name.slice(0, 1)
      }}</span>
      <span class="uc-main">
        <span class="uc-name"
          >{{ r.agent.name
          }}<small v-if="r.acc.usage?.session?.resets_at">{{
            resetsIn(r.acc.usage.session.resets_at)
          }}</small></span
        >
        <span class="uc-minis">
          <span v-for="m in windows(r.acc)" :key="m.key" class="uc-mini">
            {{ m.key }}
            <span class="bar"
              ><i :class="tone(m.w)" :style="{ width: width(m.w) }"></i></span
            ><b>{{ pct(m.w) }}%</b>
          </span>
        </span>
      </span>
      <ChevronRight class="icon uc-chev" />
    </button>
    <p v-if="!rows.length" class="num">
      No agent limits yet. Sign in to Claude or Codex in Settings.
    </p>
  </div>
  <Teleport to="body">
    <section
      v-if="opened"
      ref="panel"
      class="uc-panel"
      tabindex="-1"
      :style="pos"
      :aria-label="`${opened.agent.name} usage`"
    >
      <header>
        <span class="uc-ava" aria-hidden="true">{{
          opened.agent.name.slice(0, 1)
        }}</span>
        <b>{{ opened.agent.name }}</b>
        <small>{{ updated }}</small>
      </header>
      <div v-for="a in opened.accounts" :key="a.id" class="uc-acc">
        <strong>{{ a.label }}</strong>
        <p v-if="a.usage_error" class="uc-err">Usage unavailable</p>
        <div v-for="m in windows(a)" :key="m.key" class="uc-win">
          <span class="uc-wname">{{ m.name }}</span>
          <div
            class="bar"
            role="progressbar"
            :aria-label="`${opened.agent.name} ${m.name} limit used`"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="pct(m.w)"
          >
            <i :class="tone(m.w)" :style="{ width: width(m.w) }"></i>
          </div>
          <div class="uc-foot">
            <span>{{ pct(m.w) }}% used</span
            ><span v-if="m.w.resets_at">{{ resetsIn(m.w.resets_at) }}</span>
          </div>
        </div>
      </div>
      <RouterLink class="uc-manage" to="/settings#agents" @click="close"
        ><UserCog class="icon" />Manage accounts</RouterLink
      >
    </section>
  </Teleport>
</template>
