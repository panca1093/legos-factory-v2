<script setup lang="ts">
import { computed, ref } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { MoreHorizontal, Plus, Trash2 } from "lucide-vue-next";
import "../factory/factory.css";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import { useToast } from "../../composables/useToast";
import Popover from "../../components/ui/Popover.vue";
import ActorBadge from "../factory/ActorBadge.vue";
import DeleteDialog from "../factory/work-orders/DeleteDialog.vue";
import {
  actorOf,
  isDeletable,
  isNeedsYou,
  orderStatus,
  stageStatuses,
  stages,
} from "../../composables/presentation";
import { filterOrders, orderFilter, woPath, type OrderFilter } from "./rail";

const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter(),
  { toast } = useToast();
const projectId = computed(() => String(route.params.id));
const project = computed(() =>
  factory.projects.find((p) => p.id === projectId.value),
);
const filter = computed(() => orderFilter(route.query.filter));
const own = computed(() =>
  factory.workOrders.filter((w) => w.project_id === projectId.value),
);
const rows = computed(() =>
  [...filterOrders(own.value, filter.value)].sort(
    (a, b) =>
      Number(isNeedsYou(b)) - Number(isNeedsYou(a)) ||
      b.updated_at.localeCompare(a.updated_at),
  ),
);
const chips: { key: OrderFilter; label: string }[] = [
  { key: "open", label: "Open" },
  { key: "done", label: "Done" },
  { key: "all", label: "All" },
];
const count = (f: OrderFilter) => filterOrders(own.value, f).length;
function setFilter(f: OrderFilter) {
  void router.replace({
    query: { ...route.query, filter: f === "open" ? undefined : f },
  });
}
function ago(iso: string) {
  const m = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60000));
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  return m < 1440
    ? `${Math.round(m / 60)}h ago`
    : `${Math.round(m / 1440)}d ago`;
}
const newOrder = () =>
  void router.replace({ query: { ...route.query, new: "1" } });

const menu = ref(false),
  deleting = ref(false),
  deleteBusy = ref(false),
  deleteError = ref("");
const blocked = computed(() => {
  const n = own.value.filter((w) => !isDeletable(w)).length;
  return n
    ? `${n} work order${n === 1 ? " is" : "s are"} still active. Cancel or finish ${n === 1 ? "it" : "them"} before deleting this project.`
    : "";
});
async function confirmDelete() {
  deleteBusy.value = true;
  deleteError.value = "";
  try {
    await factory.deleteProject(projectId.value);
    deleting.value = false;
    toast("Project deleted");
    await router.replace("/projects");
  } catch (e) {
    deleteError.value = e instanceof Error ? e.message : String(e);
  } finally {
    deleteBusy.value = false;
  }
}
</script>
<template>
  <div class="fx-page">
    <div class="page-heading">
      <div>
        <div class="eyebrow">Work orders</div>
        <h1>{{ project?.name ?? "Factory" }}</h1>
      </div>
      <div class="row">
        <Popover v-if="session.canWrite" v-model:open="menu">
          <template #trigger="{ toggle }"
            ><button
              class="lg-btn small ghost"
              aria-label="Project actions"
              aria-haspopup="true"
              :aria-expanded="menu"
              @click="toggle"
            >
              <MoreHorizontal class="icon" /></button
          ></template>
          <button
            class="danger-text"
            @click="
              deleteError = '';
              deleting = true;
            "
          >
            <Trash2 class="icon" />Delete project…
          </button>
        </Popover>
        <button
          v-if="session.canWrite"
          class="lg-btn primary pill"
          data-test="overview-new"
          @click="newOrder"
        >
          <Plus class="icon" />New work order
        </button>
      </div>
    </div>
    <div class="fx-chips" role="group" aria-label="Filter work orders">
      <button
        v-for="c in chips"
        :key="c.key"
        class="fx-chip"
        :class="{ on: filter === c.key }"
        :aria-pressed="filter === c.key"
        :data-test="`chip-${c.key}`"
        @click="setFilter(c.key)"
      >
        {{ c.label }} {{ count(c.key) }}
      </button>
    </div>
    <p v-if="!rows.length" class="notice">
      {{
        filter === "done"
          ? "Nothing has shipped or been closed yet."
          : filter === "open"
            ? "No open work orders. Create one to get the crew moving."
            : "No work orders yet."
      }}
    </p>
    <ul class="fx-lane pj-orders">
      <li v-for="w in rows" :key="w.id">
        <RouterLink
          :to="woPath(projectId, w.id)"
          class="fx-order"
          :class="{ attn: isNeedsYou(w) }"
          data-test="order-row"
          :aria-labelledby="`wo-title-${w.id}`"
          :aria-describedby="`wo-status-${w.id}`"
        >
          <div class="fx-order-top">
            <span class="mono muted tiny">{{ w.id.slice(0, 12) }}</span>
            <ActorBadge :actor="actorOf(w)" />
          </div>
          <div class="fx-order-main">
            <strong :id="`wo-title-${w.id}`">{{ w.title }}</strong>
            <span class="tiny muted">updated {{ ago(w.updated_at) }}</span>
          </div>
          <div
            class="fx-segs"
            role="img"
            :aria-label="
              stages.map((s, i) => `${s}: ${stageStatuses(w)[i]}`).join(', ')
            "
          >
            <i v-for="(st, i) in stageStatuses(w)" :key="i" :class="st"></i>
          </div>
          <span
            :id="`wo-status-${w.id}`"
            class="fx-pill"
            :class="orderStatus(w).tone"
            >{{ orderStatus(w).label }}</span
          >
        </RouterLink>
      </li>
    </ul>
  </div>
  <DeleteDialog
    :open="deleting"
    title="Delete this project?"
    confirm-label="Delete project"
    :busy="deleteBusy"
    :blocked="blocked"
    :error="deleteError"
    @close="deleting = false"
    @confirm="confirmDelete"
  >
    <p class="small">
      <strong>{{ project?.name }}</strong> and
      <strong data-test="delete-count"
        >{{ own.length }} work order{{ own.length === 1 ? "" : "s" }}</strong
      >
      with their history will be permanently deleted. This cannot be undone.
    </p>
    <p class="small muted">
      Your local folders and any GitHub pull requests or branches are not
      touched.
    </p>
  </DeleteDialog>
</template>
