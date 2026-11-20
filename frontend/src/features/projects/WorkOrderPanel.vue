<script setup lang="ts">
import { computed, ref } from "vue";
import { RouterLink, useRouter } from "vue-router";
import ActorBadge from "../factory/ActorBadge.vue";
import { groupActivity } from "../factory/runs/agentActivity";
import { activityThread, handoffs } from "../factory/runs/activityThread";
import PlanProgressCard from "../factory/runs/PlanProgressCard.vue";
import HappeningCard from "../factory/runs/HappeningCard.vue";
import { useSession } from "../../stores/session";
import SidePanel, { type SideTab } from "./SidePanel.vue";
import { crew, taskState } from "../../composables/presentation";
import { useFactory } from "../../stores/factory";
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
const session = useSession();
const tasks = computed(() => factory.detail?.plan ?? []);
const id = computed(() => order.value?.id ?? "");
const canRetry = computed(
  () => session.canWrite && order.value?.state === "IMPLEMENTING",
);
const steer = computed(() =>
  session.canWrite
    ? (runID: string, m: string) => factory.steer(id.value, runID, m)
    : undefined,
);
const busy = ref(false);
async function act(fn: () => Promise<unknown>) {
  if (busy.value || !session.canWrite) return;
  busy.value = true;
  try {
    await fn();
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  } finally {
    busy.value = false;
  }
}
const pairs = computed(() =>
  order.value
    ? handoffs(
        activityThread(factory.events, order.value, tasks.value, runs.value),
      )
    : [],
);
const who = (a: string) =>
  a === "you" ? "You" : a.charAt(0).toUpperCase() + a.slice(1);
const live = computed(
  () => runs.value.filter((r) => r.state === "running").length,
);
const tabs = computed<SideTab[]>(() => [
  { key: "details", label: "Details" },
  {
    key: "plan",
    label: "Plan Progress",
    badge: tasks.value.length
      ? `${tasks.value.filter((t) => taskState(t.id, runs.value) === "completed").length}/${tasks.value.length}`
      : "",
  },
  {
    key: "happening",
    label: "What’s Happening",
    badge: live.value || "",
    hot: true,
  },
]);
const date = (iso: string) =>
  new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(iso));
</script>
<template>
  <SidePanel
    v-if="order"
    :tabs="tabs"
    initial="happening"
    label="Work order panel"
  >
    <template #details>
      <section class="sp-sec" data-card="wo-details">
        <h4 class="sp-h">Details</h4>
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
              <a
                :href="order.pr_url"
                target="_blank"
                rel="noopener noreferrer"
                >{{ order.pr_ref || "Open on host" }}</a
              >
            </dd>
          </div>
        </dl>
        <div class="wp-actions">
          <RouterLink
            v-if="prNumber"
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
        </div>
      </section>
      <section class="sp-sec" data-card="crew">
        <h4 class="sp-h">Crew</h4>
        <ul class="pc-crew">
          <li v-for="c in roles" :key="c.id" :data-role="c.id">
            <ActorBadge :actor="c.id" small />
            <span class="pc-name">{{ c.name }}</span>
            <span class="pc-state" :class="{ on: c.on }">{{ c.state }}</span>
          </li>
        </ul>
      </section>
    </template>
    <template #plan>
      <section class="sp-sec" data-card="plan-progress">
        <h4 class="sp-h">Tasks</h4>
        <PlanProgressCard
          v-if="tasks.length"
          bare
          :tasks="tasks"
          :runs="runs"
          :can-retry="canRetry"
          :revision="order.revision"
          @retry="(taskID) => act(() => factory.retryTask(id, taskID))"
          @retry-remediation="act(() => factory.retryRemediation(id))"
        />
        <p v-else class="tiny muted">No plan yet.</p>
      </section>
      <section class="sp-sec" data-card="handoffs">
        <h4 class="sp-h">Handoffs &amp; loops</h4>
        <div class="wp-loop">
          <div v-for="h in pairs" :key="h.from + h.to" class="wp-loop-row">
            <span>{{ who(h.from) }} → {{ who(h.to) }}</span>
            <span class="tiny muted"
              ><template v-if="h.tasks"
                >{{ h.tasks }} task{{ h.tasks === 1 ? "" : "s" }}</template
              ><template v-if="h.tasks && h.feedback"> · </template
              ><template v-if="h.feedback"
                >{{ h.feedback }} feedback</template
              ></span
            >
          </div>
          <p v-if="!pairs.length" class="tiny muted">No handoffs yet.</p>
        </div>
      </section>
    </template>
    <template #happening>
      <HappeningCard
        bare
        :events="factory.events"
        :runs="runs"
        :tasks="tasks"
        :has-more="factory.eventsHasMore"
        :steer="steer"
      />
    </template>
  </SidePanel>
</template>
<style scoped>
.wp-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.wp-loop {
  display: flex;
  flex-direction: column;
  gap: 10px;
  font-size: 13px;
}
.wp-loop-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
</style>
