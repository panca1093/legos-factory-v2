<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { ArrowLeft, ChevronRight, PanelLeftClose, Plus } from "lucide-vue-next";
import "./projects.css";
import { api, ApiError, type ProjectRail } from "../../types/api";
import {
  chipText,
  connectionsPath,
  errCode,
  errMessage,
  pullPath,
  type RailPulls,
} from "../../types/review";
import {
  eventTarget,
  isPRReviewEvent,
  usePRRefresh,
} from "../../stores/reviews";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import IconButton from "../../components/ui/IconButton.vue";
import UsageCard from "../../app/layouts/UsageCard.vue";
import { projectColor } from "../../composables/presentation";
import {
  capRail,
  railDot,
  readFlags,
  repoLabel,
  sortRail,
  woPath,
  writeStored,
} from "./rail";

defineEmits<{ hide: [] }>();
const route = useRoute(),
  router = useRouter(),
  factory = useFactory(),
  session = useSession();
const sectionsKey = "legos-project-rail-sections";
const projectId = computed(() => String(route.params.id ?? ""));
const selectedWO = computed(() => String(route.params.wo ?? ""));
const rail = ref<ProjectRail | null>(null),
  missing = ref(false),
  failed = ref(false),
  more = ref<Record<string, boolean>>({}),
  collapsed = ref<Record<string, boolean>>(readFlags(sectionsKey));
let seq = 0,
  pullsSeq = 0,
  timer: ReturnType<typeof setTimeout> | undefined;

const project = computed(() =>
  factory.projects.find((p) => p.id === projectId.value),
);
const name = computed(
  () => rail.value?.project?.name ?? project.value?.name ?? "",
);
const repo = computed(
  () =>
    rail.value?.project?.repo ||
    (project.value ? repoLabel(project.value) : ""),
);
const initials = computed(
  () =>
    name.value
      .split(/[\s\-_/]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0])
      .join("")
      .toUpperCase() || "?",
);
const tileColor = computed(() =>
  projectColor(factory.projects.findIndex((p) => p.id === projectId.value)),
);
const rows = computed(() => sortRail(rail.value?.work_orders?.rows ?? []));
const open = computed(() => rail.value?.work_orders?.open ?? rows.value.length);
const needsYou = computed(() => rows.value.some((r) => r.waiting_on_you));
const factoryRows = computed(() =>
  capRail(rows.value, !!more.value.factory, selectedWO.value),
);
const selectedPull = computed(() => Number(route.params.number ?? 0));
const pulls = computed(
  () => rail.value?.pulls as unknown as RailPulls | undefined,
);
const pullsCode = computed(() => errCode(pulls.value?.error));
const noIdentity = computed(() => pullsCode.value === "no_identity");
const pullsWarn = computed(() =>
  pullsCode.value && pullsCode.value !== "no_identity"
    ? errMessage(pulls.value?.error)
    : "",
);
const pullRows = computed(() =>
  capRail(
    (pulls.value?.rows ?? []).map((r) => ({ ...r, id: String(r.number) })),
    !!more.value.pulls,
    String(selectedPull.value),
  ),
);
const isOpen = (key: string) => !collapsed.value[key];
function toggle(key: string) {
  collapsed.value = { ...collapsed.value, [key]: isOpen(key) };
  writeStored(sectionsKey, JSON.stringify(collapsed.value));
}

async function load(part: "all" | "wo" = "all") {
  const id = projectId.value,
    mine = ++seq;
  try {
    const res = await api.get<ProjectRail>(
      `/api/v1/projects/${id}/rail?part=${part}`,
    );
    if (mine !== seq) return;
    rail.value =
      part === "all" || !rail.value
        ? res
        : { ...rail.value, project: res.project, work_orders: res.work_orders };
    missing.value = failed.value = false;
  } catch (e) {
    if (mine !== seq) return;
    missing.value = e instanceof ApiError && e.status === 404;
    failed.value = !missing.value;
    if (missing.value) rail.value = null;
  }
}
async function loadPulls() {
  const id = projectId.value,
    mine = ++pullsSeq;
  try {
    const res = await api.get<ProjectRail>(
      `/api/v1/projects/${id}/rail?part=pulls`,
    );
    if (mine !== pullsSeq || !rail.value) return;
    rail.value = { ...rail.value, pulls: res.pulls };
  } catch {
    /* The cached rows stay; the next refetch retries. */
  }
}
usePRRefresh(
  () => void loadPulls(),
  (ev) => {
    const t = eventTarget(ev);
    return !t.project || t.project === projectId.value;
  },
);
function schedule() {
  clearTimeout(timer);
  timer = setTimeout(() => void load("wo"), 1000);
}
watch(
  projectId,
  () => {
    clearTimeout(timer);
    pullsSeq++;
    rail.value = null;
    missing.value = failed.value = false;
    more.value = {};
    void load();
  },
  { immediate: true },
);
watch(
  () => factory.workOrders.map((w) => w.id),
  (now, old) => {
    if (old.some((id) => !now.includes(id))) void load("wo");
  },
);
watch(
  () => factory.liveEvents[0],
  (ev) => {
    if (!ev || isPRReviewEvent(ev)) return;
    const wo = factory.workOrders.find((w) => w.id === ev.work_order_id);
    if (!wo || wo.project_id === projectId.value) schedule();
  },
);
onBeforeUnmount(() => {
  clearTimeout(timer);
  seq++;
  pullsSeq++;
});
const newOrder = () =>
  void router.replace({ query: { ...route.query, new: "1" } });
</script>
<template>
  <div class="pr" tabindex="-1">
    <div class="rail-head">
      <RouterLink to="/projects" class="pr-back"
        ><ArrowLeft class="icon" />All projects</RouterLink
      >
      <IconButton label="Hide sidebar" @click="$emit('hide')"
        ><PanelLeftClose class="icon"
      /></IconButton>
    </div>
    <div class="pr-id">
      <span
        v-if="!missing"
        class="pr-tile"
        :style="{ '--c': tileColor }"
        aria-hidden="true"
        >{{ initials }}</span
      >
      <div class="pr-id-text">
        <div class="pr-name">
          <strong>{{ missing ? "Project not found" : name }}</strong><i
            v-if="needsYou"
            class="pr-attn"
            title="Waiting on you"
            aria-label="Waiting on you"
          ></i>
        </div>
        <small v-if="repo && !missing" class="pr-repo">{{ repo }}</small>
      </div>
    </div>
    <nav class="pr-nav grow" aria-label="Project">
      <p v-if="missing" class="empty-note">
        This project does not exist or you do not have access.
      </p>
      <template v-else>
        <section class="pr-sec">
          <div class="pr-sec-head">
            <button
              type="button"
              class="pr-chev"
              :aria-expanded="isOpen('factory')"
              aria-label="Toggle Factory section"
              @click="toggle('factory')"
            >
              <ChevronRight class="icon" />
            </button>
            <RouterLink
              :to="`/projects/${projectId}`"
              class="pr-title"
              data-test="rail-factory"
              >Factory</RouterLink
            ><span class="pr-count"
              >{{ open }}<span class="sr-only"> open work orders</span></span
            >
            <IconButton
              v-if="session.canWrite"
              label="Add a work order"
              data-test="rail-new"
              @click="newOrder"
              ><Plus class="icon"
            /></IconButton>
          </div>
          <div v-if="isOpen('factory')" class="pr-rows">
            <RouterLink
              v-for="r in factoryRows.shown"
              :key="r.id"
              :to="woPath(projectId, r.id)"
              class="pr-row"
              :class="{ active: r.id === selectedWO }"
              :aria-current="r.id === selectedWO ? 'page' : undefined"
              data-test="rail-wo"
              ><i class="pr-dot" :class="railDot(r)" aria-hidden="true"></i
              ><span class="t">{{ r.title }}</span
              ><span v-if="r.waiting_on_you" class="pr-you"
                >you</span
              ></RouterLink
            >
            <p v-if="!rows.length && !failed" class="empty-note">
              {{ rail ? "No open work orders." : "Loading…" }}
            </p>
            <p v-if="failed" class="empty-note" role="alert">
              Could not load work orders.
            </p>
            <button
              v-if="factoryRows.hidden || more.factory"
              type="button"
              class="rt-more"
              @click="more = { ...more, factory: !more.factory }"
            >
              {{
                more.factory ? "Show less" : `Show ${factoryRows.hidden} more`
              }}
            </button>
          </div>
        </section>
        <section class="pr-sec">
          <div class="pr-sec-head">
            <button
              type="button"
              class="pr-chev"
              :aria-expanded="isOpen('pulls')"
              aria-label="Toggle Pull requests section"
              @click="toggle('pulls')"
            >
              <ChevronRight class="icon" />
            </button>
            <RouterLink
              :to="`/projects/${projectId}/pulls`"
              class="pr-title"
              data-test="rail-pulls-title"
              >Pull requests</RouterLink
            ><span v-if="pulls && !noIdentity" class="pr-count"
              >{{ pulls.count
              }}<span class="sr-only"> pull requests related to you</span></span
            ><i
              v-if="pullsWarn"
              class="pr-warn"
              role="img"
              :title="pullsWarn"
              :aria-label="pullsWarn"
              data-test="rail-pulls-warn"
            ></i>
          </div>
          <div v-if="isOpen('pulls')" class="pr-rows" data-test="rail-pulls">
            <RouterLink
              v-if="noIdentity"
              :to="connectionsPath"
              class="pr-row"
              data-test="rail-connect"
              ><span class="t">Connect an account</span></RouterLink
            >
            <template v-else>
              <RouterLink
                v-for="r in pullRows.shown"
                :key="r.number"
                :to="pullPath(projectId, r.number)"
                class="pr-row"
                :class="{
                  active: r.number === selectedPull,
                  quiet: r.label === 'none' && !r.running,
                }"
                :aria-current="r.number === selectedPull ? 'page' : undefined"
                data-test="rail-pr"
                ><span class="mono pr-num">#{{ r.number }}</span
                ><span class="t">{{ r.title }}</span
                ><span
                  v-if="chipText(r)"
                  class="pr-label"
                  :class="[r.label, { running: r.running }]"
                  >{{ chipText(r) }}</span
                ></RouterLink
              >
              <p v-if="!pulls" class="empty-note">
                {{ failed ? "Could not load pull requests." : "Loading…" }}
              </p>
              <p
                v-else-if="!pulls.rows?.length && !pullsWarn"
                class="empty-note"
              >
                No pull requests need you.
              </p>
              <button
                v-if="pullRows.hidden || more.pulls"
                type="button"
                class="rt-more"
                @click="more = { ...more, pulls: !more.pulls }"
              >
                {{ more.pulls ? "Show less" : `Show ${pullRows.hidden} more` }}
              </button>
              <RouterLink
                v-if="pulls"
                :to="`/projects/${projectId}/pulls`"
                class="pr-all"
                data-test="rail-pulls-all"
                >All pull requests</RouterLink
              >
            </template>
          </div>
        </section>
        <section class="pr-sec">
          <div class="pr-sec-head">
            <button
              type="button"
              class="pr-chev"
              :aria-expanded="isOpen('code')"
              aria-label="Toggle Code section"
              @click="toggle('code')"
            >
              <ChevronRight class="icon" />
            </button>
            <RouterLink :to="`/projects/${projectId}/code`" class="pr-title"
              >Code</RouterLink
            >
          </div>
          <div v-if="isOpen('code')" class="pr-rows">
            <p class="pr-muted">Editor coming soon</p>
          </div>
        </section>
      </template>
    </nav>
    <UsageCard />
  </div>
</template>
<style scoped>
.pr {
  gap: var(--space-3);
}
.pr-back {
  font-size: 12px;
  font-weight: 600;
  padding: 4px 8px 4px 4px;
  margin-left: -4px;
  border-radius: var(--radius-full);
  transition:
    background var(--dur-fast),
    color var(--dur-fast);
}
.pr-back:hover {
  background: var(--surface-2);
}
.pr-id {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  background: var(--canvas);
  box-shadow: var(--shadow-card);
}
.pr-id-text {
  min-width: 0;
  display: grid;
  gap: 2px;
}
.pr-tile {
  flex: none;
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--c) 22%, var(--surface));
  color: var(--ink);
  border: 1px solid color-mix(in srgb, var(--c) 45%, transparent);
  font: 700 13px var(--font-display);
  letter-spacing: 0.02em;
}
.pr-name {
  font: 600 16px/1.2 var(--font-display);
  letter-spacing: -0.01em;
  min-width: 0;
}
.pr-name strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pr-attn {
  flex: none;
  box-shadow: 0 0 0 3px var(--warning-soft);
}
.pr-repo {
  font: 11px var(--font-mono);
  color: var(--muted);
  overflow-wrap: anywhere;
}
.pr-nav {
  gap: var(--space-4);
  padding: 2px 0;
}
.pr-sec-head {
  gap: var(--space-1);
  padding-bottom: 2px;
  border-bottom: 1px solid var(--line);
}
.pr-chev {
  border-radius: var(--radius-xs);
  transition: background var(--dur-fast);
}
.pr-chev:hover {
  background: var(--surface-2);
  color: var(--ink);
}
.pr-title {
  padding: 6px 0;
  font-size: 11px;
  letter-spacing: 0.08em;
  color: var(--ink);
}
.pr-count {
  flex: none;
  min-width: 20px;
  padding: 1px 7px;
  border-radius: var(--radius-full);
  background: var(--tint);
  color: var(--accent);
  font-size: 11px;
  font-weight: 650;
  text-align: center;
}
.pr-sec-head :deep(.lg-icon-btn) {
  width: 26px;
  height: 26px;
  margin-left: var(--space-1);
  border-radius: var(--radius-sm);
  border: 1px solid var(--line);
  background: var(--surface);
  color: var(--accent);
  transition:
    background var(--dur-fast),
    border-color var(--dur-fast);
}
.pr-sec-head :deep(.lg-icon-btn:hover) {
  background: var(--tint);
  border-color: var(--line-strong);
}
.pr-rows {
  gap: 2px;
  margin: var(--space-2) 0 0;
}
.pr-row {
  position: relative;
  gap: 10px;
  padding: 7px 10px 7px 12px;
  border-radius: var(--radius-sm);
  color: var(--muted);
  transition:
    background var(--dur-fast),
    color var(--dur-fast);
}
.pr-row:hover {
  background: var(--surface-2);
  color: var(--ink);
}
.pr-row.active {
  background: var(--tint);
  color: var(--ink);
  font-weight: 600;
}
.pr-row.active::before {
  content: "";
  position: absolute;
  left: 0;
  top: 7px;
  bottom: 7px;
  width: 3px;
  border-radius: var(--radius-full);
  background: var(--accent);
}
.pr-dot {
  background: var(--success);
  box-shadow: 0 0 0 3px var(--success-soft);
}
.pr-dot.waiting {
  background: var(--warning);
  box-shadow: 0 0 0 3px var(--warning-soft);
}
.pr-dot.failed {
  background: var(--danger);
  box-shadow: 0 0 0 3px var(--danger-soft);
}
.pr-dot.queued {
  background: var(--line-strong);
  box-shadow: 0 0 0 3px var(--surface-2);
}
.pr-you {
  padding: 1px 8px;
  font-size: 10px;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}
.pr-warn {
  flex: none;
  width: 8px;
  height: 8px;
  margin-left: var(--space-1);
  border-radius: 50%;
  background: var(--warning);
  box-shadow: 0 0 0 3px var(--warning-soft);
}
.pr-num {
  flex: none;
  font-size: 11px;
}
.pr-row.quiet {
  opacity: 0.65;
}
.pr-label {
  flex: none;
  padding: 1px 8px;
  border-radius: var(--radius-full);
  background: var(--surface-2);
  color: var(--muted);
  font-size: 10px;
  font-weight: 650;
  letter-spacing: 0.04em;
}
.pr-label.ai,
.pr-label.running {
  background: var(--tint);
  color: var(--accent);
}
.pr-label.new,
.pr-label.approve {
  background: var(--warning-soft);
  color: var(--warning);
}
.pr-all {
  padding: 4px 12px;
  font-size: 12px;
  font-weight: 600;
  color: var(--accent);
}
.pr-all:hover {
  text-decoration: underline;
}
.pr-muted {
  padding: 6px 12px;
  font-style: normal;
  opacity: 0.8;
}
.pr .rt-more {
  padding: 4px 12px;
  font-weight: 600;
  border-radius: var(--radius-sm);
}
.pr .rt-more:hover {
  text-decoration: underline;
}
.pr .empty-note {
  padding: 6px 12px;
}
</style>
