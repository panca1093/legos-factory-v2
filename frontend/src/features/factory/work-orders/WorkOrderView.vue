<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { ArrowLeft, GitBranch } from "lucide-vue-next";
import "../factory.css";
import { useFactory } from "../../../stores/factory";
import { useSession } from "../../../stores/session";
import { subscribe } from "../../../services/events/sse";
import AppDialog from "../../../components/ui/AppDialog.vue";
import {
  money,
  orderStatus,
  shipState,
  stageIndex,
  stageTabs,
} from "../../../composables/presentation";
import BlueprintPanel from "./BlueprintPanel.vue";
import StageTrack from "./StageTrack.vue";
import NowCard from "./NowCard.vue";
import TaskGraph from "../task-graph/TaskGraph.vue";
import ActivityPanel from "../runs/ActivityPanel.vue";
import ReviewPanel from "../reviews/ReviewPanel.vue";
import ChangesPanel from "../reviews/ChangesPanel.vue";
import { escalationReason } from "../runs/agentActivity";
const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter();
const tabs = [
  { key: "plan", label: "Plan" },
  { key: "progress", label: "Progress" },
  { key: "review", label: "Review" },
  { key: "changes", label: "Changes & PR" },
];
const legacyTabs: Record<string, string> = {
  blueprint: "plan",
  graph: "progress",
  activity: "progress",
  quality: "review",
};
const id = computed(() => String(route.params.id)),
  order = computed(() => factory.detail?.work_order),
  tasks = computed(() => factory.detail?.plan ?? []),
  runs = computed(() => factory.detail?.runs ?? []);
// Pinned on first load so the page doesn't change tab while the order advances.
const landing = ref("");
const tab = computed(() => {
  const q = String(route.query.tab ?? ""),
    key = legacyTabs[q] ?? q;
  return tabs.some((t) => t.key === key) ? key : landing.value || "plan";
});
watch(
  [order, runs],
  () => {
    if (order.value && !landing.value)
      landing.value = stageTabs[stageIndex(order.value, runs.value)];
  },
  { immediate: true },
);
const project = computed(() =>
  factory.projects.find((p) => p.id === order.value?.project_id),
);
const canClarify = computed(
  () =>
    session.canWrite &&
    (session.isAdmin || session.user?.id === order.value?.created_by),
);
const busy = ref(false),
  actionError = ref(""),
  modal = ref<
    | "clarify"
    | "revise"
    | "approve"
    | "request_changes"
    | "decline"
    | "cancel"
    | null
  >(null),
  notes = ref("");
const modalTitle = computed(
  () =>
    ({
      clarify: "Architect needs your input",
      revise: "Refine the blueprint",
      approve: "Your decision closes the loop",
      request_changes: "Request a focused revision",
      decline: "Close this proposed change?",
      cancel: "Cancel this work order?",
    })[modal.value ?? "approve"],
);
const status = computed(() => order.value && orderStatus(order.value)),
  ship = computed(() => (order.value ? shipState(order.value) : null)),
  reason = computed(() => escalationReason(factory.events)),
  cost = computed(() => runs.value.reduce((n, r) => n + r.cost_micros, 0));
watch(
  id,
  async (value, _old, onCleanup) => {
    factory.clearDetail();
    landing.value = "";
    actionError.value = "";
    modal.value = null;
    let active = true;
    const unsubscribe = subscribe(`/api/v1/work-orders/${value}/events`, () => {
      if (active && factory.selectedID === value) void factory.reloadDetail();
    });
    onCleanup(() => {
      active = false;
      unsubscribe();
    });
    await factory.loadDetail(value);
  },
  { immediate: true },
);
onBeforeUnmount(() => factory.clearDetail());
async function act(fn: () => Promise<unknown>) {
  if (busy.value || !session.canWrite) return;
  busy.value = true;
  actionError.value = "";
  try {
    await fn();
    modal.value = null;
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
function open(value: typeof modal.value) {
  notes.value = "";
  actionError.value = "";
  modal.value = value;
}
async function decision() {
  if (!modal.value) return;
  const kind = modal.value;
  if (
    ["clarify", "revise", "request_changes", "decline"].includes(kind) &&
    !notes.value.trim()
  ) {
    actionError.value =
      "Record the decision or requested change before continuing.";
    return;
  }
  await act(() =>
    kind === "clarify"
      ? factory.answerClarification(id.value, notes.value.trim())
      : kind === "revise"
        ? factory.planDecision(id.value, "revise", notes.value.trim())
        : kind === "cancel"
          ? factory.cancel(id.value)
          : factory.prDecision(
              id.value,
              kind as "approve" | "request_changes" | "decline",
              notes.value.trim(),
            ),
  );
}
function setTab(key: string) {
  void router.replace({ query: { ...route.query, tab: key } });
}
function goStage(i: number) {
  setTab(stageTabs[i]);
}
function tabKeys(event: KeyboardEvent, index: number) {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
  event.preventDefault();
  const next =
    event.key === "Home"
      ? 0
      : event.key === "End"
        ? tabs.length - 1
        : (index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) %
          tabs.length;
  setTab(tabs[next].key);
  document.getElementById("order-tab-" + tabs[next].key)?.focus();
}
function download() {
  if (!order.value) return;
  const o = order.value;
  const content = [
    `# Blueprint: ${o.title}`,
    `Work order: ${o.id}\nRevision: ${o.revision}\nState: ${o.state}`,
    `## Outcome\n${o.objective}`,
    `## Acceptance contract\n${o.acceptance}`,
    `## Human decision\n${o.clarif_answer || "No clarification recorded."}`,
    `## Plan\n${tasks.value.map((t) => `- ${t.title} (${t.role}): ${t.objective}; dependencies: ${(t.deps ?? []).join(", ") || "none"}`).join("\n")}`,
  ].join("\n\n");
  const url = URL.createObjectURL(
    new Blob([content], { type: "text/markdown" }),
  );
  const a = document.createElement("a");
  a.href = url;
  a.download = `${o.id}-blueprint.md`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
</script>
<template>
  <div v-if="factory.error" class="error-state" role="alert">
    <h2>We couldn’t load this work order.</h2>
    <p class="small">{{ factory.error }}</p>
    <button class="btn" @click="factory.loadDetail(id)">Try again</button>
    <RouterLink class="btn" to="/factory">Back to Factory</RouterLink>
  </div>
  <div v-else-if="!order" class="panel empty" aria-busy="true">
    Loading work order…
  </div>
  <div v-else class="fx-page">
    <div class="fx-head">
      <RouterLink to="/factory" class="fx-back" aria-label="Back to factory"
        ><ArrowLeft class="icon"
      /></RouterLink>
      <div class="grow">
        <div class="fx-crumb">
          <RouterLink to="/factory">Factory</RouterLink> /
          {{ project?.name || "Project" }} ·
          <span class="mono">{{ order.id }}</span>
        </div>
        <h1>{{ order.title }}</h1>
        <div class="fx-meta">
          <span
            ><GitBranch class="icon" />{{
              project?.repo_url || "Repository not reported"
            }}</span
          ><span>Revision {{ order.revision }}</span
          ><span>{{ money(cost) }} · {{ runs.length }} runs</span>
        </div>
      </div>
      <span class="fx-pill" :class="status?.tone">{{ status?.label }}</span>
    </div>
    <StageTrack :order="order" :runs="runs" @select="goStage" />
    <p
      v-if="route.query.planningError && order.state === 'CREATED'"
      class="notice"
      role="alert"
    >
      Your work order was saved, but planning could not start:
      {{ route.query.planningError }}. Retry with Draft blueprint below.
    </p>
    <div v-if="actionError && !modal" class="error-state" role="alert">
      {{ actionError }}
    </div>
    <NowCard
      :order="order"
      :tasks="tasks"
      :runs="runs"
      :events="factory.events"
      :can-write="session.canWrite"
      :can-clarify="canClarify"
      :busy="busy"
      @draft="act(() => factory.planGenerate(id))"
      @approve-plan="act(() => factory.planDecision(id, 'approve'))"
      @revise-plan="open('revise')"
      @clarify="open('clarify')"
      @pause="act(() => factory.pause(id))"
      @resume="act(() => factory.resume(id))"
      @cancel="open('cancel')"
      @goto="setTab"
    />
    <div class="fx-tabs" role="tablist" aria-label="Work order sections">
      <button
        v-for="(t, i) in tabs"
        :id="'order-tab-' + t.key"
        :key="t.key"
        role="tab"
        :tabindex="tab === t.key ? 0 : -1"
        :aria-selected="tab === t.key"
        :aria-controls="'order-panel-' + t.key"
        :class="{ active: tab === t.key }"
        @click="setTab(t.key)"
        @keydown="tabKeys($event, i)"
      >
        {{ t.label }}
      </button>
    </div>
    <div
      :id="'order-panel-' + tab"
      role="tabpanel"
      :aria-labelledby="'order-tab-' + tab"
    >
      <BlueprintPanel
        v-if="tab === 'plan'"
        :order="order"
        :tasks="tasks"
        @export="download"
      />
      <div v-else-if="tab === 'progress'" class="fx-stack">
        <TaskGraph :tasks="tasks" :runs="runs" />
        <ActivityPanel
          :events="factory.events"
          :runs="runs"
          :tasks="tasks"
          :can-retry="session.canWrite && order.state === 'IMPLEMENTING'"
          :revision="order.revision"
          :steer="
            session.canWrite
              ? (runID: string, m: string) => factory.steer(id, runID, m)
              : undefined
          "
          @retry="(taskID) => act(() => factory.retryTask(id, taskID))"
          @retry-remediation="act(() => factory.retryRemediation(id))"
        />
      </div>
      <ReviewPanel
        v-else-if="tab === 'review'"
        :checks="factory.quality"
        :findings="factory.findings"
        :revision="order.revision"
        :events="factory.events"
        :state="order.state"
        :can-write="session.canWrite"
        :busy="busy"
        @finding="
          (f, action, note) =>
            act(() => factory.decideFinding(id, f, action, note))
        "
      />
      <ChangesPanel
        v-else
        :runs="runs"
        :state="order.state"
        :can-write="session.canWrite"
        :busy="busy"
        :ship="ship"
        :reason="reason"
        :publish-to="project?.git_connection_id ? project.remote_repo : ''"
        :pr-url="order.pr_url"
        :pr-ref="order.pr_ref"
        :source-ref="order.source_ref"
        @decision="open"
      />
    </div>
  </div>
  <AppDialog
    :open="modal !== null"
    :title="modalTitle"
    @close="!busy && (modal = null)"
    ><div class="stack">
      <p v-if="modal === 'clarify'" class="small">
        {{ order?.clarif_question }}
      </p>
      <p v-else-if="modal === 'cancel'" class="small">
        Cancellation stops this work order. Its history and evidence stay
        available.
      </p>
      <p v-else-if="modal === 'approve'" class="notice">
        {{
          project?.git_connection_id
            ? "Approving pushes the verified commit to a work branch and opens a pull request. The work order finishes when that pull request is merged."
            : "This records your local approval for the current work order. No pull request is created."
        }}
      </p>
      <p v-else class="small muted">
        Record what should change and why. Your note stays in the work-order
        history.
      </p>
      <div v-if="modal !== 'cancel'" class="form-group">
        <label for="decision-notes">{{
          modal === "clarify"
            ? "Your answer"
            : modal === "approve"
              ? "Decision note (optional)"
              : "Decision or requested change"
        }}</label
        ><textarea
          id="decision-notes"
          v-model="notes"
          rows="4"
          placeholder="Write the precise behavior or reasoning…"
        ></textarea>
      </div>
      <p v-if="actionError" class="form-error" role="alert">
        {{ actionError }}
      </p>
    </div>
    <template #footer
      ><button class="btn" :disabled="busy" @click="modal = null">Back</button
      ><button
        class="btn primary"
        data-test="save-decision"
        :disabled="busy || !session.canWrite"
        @click="decision"
      >
        {{
          busy
            ? "Saving…"
            : modal === "clarify"
              ? "Answer & re-review"
              : modal === "approve"
                ? "Record approval"
                : modal === "cancel"
                  ? "Cancel work order"
                  : modal === "revise"
                    ? "Send revision to crew"
                    : modal === "decline"
                      ? "Record decline"
                      : "Request changes"
        }}
      </button></template
    ></AppDialog
  >
</template>
