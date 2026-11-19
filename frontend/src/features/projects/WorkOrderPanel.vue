<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { RouterLink, useRouter } from "vue-router";
import ActorBadge from "../factory/ActorBadge.vue";
import { groupActivity } from "../factory/runs/agentActivity";
import { woPanelSlots } from "./woPanelSlots";
import { crew } from "../../composables/presentation";
import { useFactory } from "../../stores/factory";
import PanelCard from "./PanelCard.vue";
import { pullPath } from "../../types/review";
import { useToast } from "../../composables/useToast";
import { useEditor } from "../editor/editorStore";
import { ApiError } from "../../types/api";

const prNumber = computed(() => {
  const o = order.value;
  if (!o?.pr_url || o.pr_merged_at || o.state === "CANCELLED") return 0;
  const m = /\/pull(?:-requests|s)?\/(\d+)|\/merge_requests\/(\d+)/.exec(
    o.pr_url,
  );
  return Number(m?.[1] ?? m?.[2] ?? 0);
});

const factory = useFactory();
const router = useRouter(),
  editor = useEditor(),
  { toast } = useToast();
const opening = ref(false);
async function openInCode() {
  const o = order.value;
  if (!o || opening.value) return;
  opening.value = true;
  try {
    const folder = await editor.openWorktree(o.id);
    await router.push(`/projects/${o.project_id}/code/${folder.id}`);
  } catch (e) {
    const gone =
      e instanceof ApiError &&
      (e.code === "FOLDER_GONE" || e.status === 404 || e.status === 410);
    toast(
      gone
        ? "This work order's worktree no longer exists."
        : e instanceof Error
          ? e.message
          : String(e),
    );
  } finally {
    opening.value = false;
  }
}
const order = computed(() => factory.detail?.work_order);
const runs = computed(() => factory.detail?.runs ?? []);
const groups = computed(() => groupActivity(factory.events));
const fromRun = (state: string) =>
  state === "running"
    ? "working"
    : state === "succeeded"
      ? "finished"
      : state === "failed"
        ? "failed"
        : "idle";
const roles = computed(() =>
  crew
    .filter((c) => c.id !== "cataloger")
    .map((c) => {
      const last = [...groups.value]
        .reverse()
        .find((g) => g.type === "run" && g.actor === c.id);
      const lastRun = runs.value.filter((r) => r.kind === c.id).at(-1);
      const state =
        last && last.type === "run"
          ? last.status === "running"
            ? "working"
            : last.status
          : lastRun
            ? fromRun(lastRun.state)
            : "idle";
      return { ...c, state, on: state === "working" };
    }),
);
const setTop = (el: unknown) =>
  (woPanelSlots.top.value = el as HTMLElement | null);
const setMid = (el: unknown) =>
  (woPanelSlots.mid.value = el as HTMLElement | null);
onBeforeUnmount(() => {
  woPanelSlots.top.value = null;
  woPanelSlots.mid.value = null;
});
const date = (iso: string) =>
  new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(iso));
</script>
<template>
  <template v-if="order">
    <div :ref="setTop" class="wp-slot"></div>
    <PanelCard id="crew" title="Crew">
      <ul class="pc-crew">
        <li v-for="c in roles" :key="c.id" :data-role="c.id">
          <ActorBadge :actor="c.id" small />
          <span class="pc-name">{{ c.name }}</span>
          <span class="pc-state" :class="{ on: c.on }">{{ c.state }}</span>
        </li>
      </ul>
    </PanelCard>
    <div :ref="setMid" class="wp-slot"></div>
    <PanelCard id="wo-details" title="Details">
      <dl class="pc-dl">
        <div>
          <dt>Branch</dt>
          <dd class="mono">legos/{{ order.id }}</dd>
        </div>
        <div>
          <dt>Base branch</dt>
          <dd class="mono">{{ order.base_branch || "default" }}</dd>
        </div>
        <div>
          <dt>Started</dt>
          <dd>{{ date(order.created_at) }}</dd>
        </div>
        <div v-if="order.pr_url">
          <dt>Pull request</dt>
          <dd>
            <a :href="order.pr_url" target="_blank" rel="noopener noreferrer">{{
              order.pr_ref || "Open on host"
            }}</a>
          </dd>
        </div>
      </dl>
      <RouterLink
        v-if="order && prNumber"
        :to="pullPath(order.project_id, prNumber)"
        class="lg-btn small"
        data-test="wo-review-pr"
        >Review PR #{{ prNumber }}</RouterLink
      >
      <button
        type="button"
        class="lg-btn small"
        data-test="wo-open-code"
        :disabled="opening"
        @click="openInCode"
      >
        Open in Code
      </button>
    </PanelCard>
  </template>
</template>
<style scoped>
.wp-slot {
  display: contents;
}
</style>
