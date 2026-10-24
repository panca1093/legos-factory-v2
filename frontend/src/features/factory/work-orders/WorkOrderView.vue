<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import {
  ArrowLeft,
  Ban,
  GitBranch,
  Clock,
  ShieldCheck,
  GitPullRequest,
  FileText,
} from "lucide-vue-next";
import { useFactory } from "../../../stores/factory";
import { useSession } from "../../../stores/session";
import { subscribe } from "../../../services/events/sse";
import StatusBadge from "../../../components/ui/StatusBadge.vue";
import AppDialog from "../../../components/ui/AppDialog.vue";
import BlueprintPanel from "./BlueprintPanel.vue";
import WorkOrderSidebar from "./WorkOrderSidebar.vue";
import TaskGraph from "../task-graph/TaskGraph.vue";
import ActivityPanel from "../runs/ActivityPanel.vue";
import RunBanner from "../runs/RunBanner.vue";
import QualityPanel from "../reviews/QualityPanel.vue";
import ChangesPanel from "../reviews/ChangesPanel.vue";
const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter();
const tabs = [
  { key: "blueprint", label: "Blueprint", icon: FileText },
  { key: "graph", label: "Task graph", icon: GitBranch },
  { key: "activity", label: "Activity", icon: Clock },
  { key: "quality", label: "Quality", icon: ShieldCheck },
  { key: "changes", label: "Changes & PR", icon: GitPullRequest },
];
const id = computed(() => String(route.params.id)),
  order = computed(() => factory.detail?.work_order),
  tasks = computed(() => factory.detail?.plan ?? []),
  runs = computed(() => factory.detail?.runs ?? []);
const tab = computed(() =>
  tabs.some((t) => t.key === route.query.tab)
    ? String(route.query.tab)
    : "blueprint",
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
const canCancel = computed(
  () =>
    order.value &&
    !["READY", "CANCELLED", "BLOCKED"].includes(order.value.state),
);
watch(
  id,
  async (value, _old, onCleanup) => {
    factory.clearDetail();
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
  <div v-else>
    <RouterLink to="/factory" class="detail-back"
      ><ArrowLeft class="icon" />Back to factory</RouterLink
    >
    <div class="detail-title">
      <div>
        <div class="eyebrow">
          {{ project?.name || "Project" }} / {{ order.id }}
        </div>
        <h1>{{ order.title }}</h1>
      </div>
      <StatusBadge :state="order.state" />
    </div>
    <div class="detail-meta">
      <span class="row" style="gap: 5px"
        ><GitBranch class="icon" />{{
          project?.repo_url || "Repository not reported"
        }}</span
      ><span>Revision {{ order.revision }}</span
      ><span class="pill">{{ order.autonomy }}</span
      ><span class="grow"></span
      ><button
        v-if="session.canWrite && canCancel"
        class="btn text small"
        :disabled="busy"
        @click="open('cancel')"
      >
        <Ban class="icon" />Cancel
      </button>
    </div>
    <div
      v-if="route.query.planningError && order.state === 'CREATED'"
      class="notice"
      role="alert"
      style="margin-bottom: 20px"
    >
      Your work order was saved, but planning could not start:
      {{ route.query.planningError }}. Retry with Draft blueprint below.
    </div>
    <div v-if="actionError && !modal" class="error-state" role="alert">
      {{ actionError }}
    </div>
    <div class="detail-layout">
      <div class="detail-main">
        <div class="tabs" role="tablist" aria-label="Work order sections">
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
          <RunBanner
            v-if="tab === 'graph' || tab === 'activity'"
            :state="order.state"
            :tasks="tasks"
            :runs="runs"
          />
          <Transition name="view" mode="out-in"
            ><BlueprintPanel
              v-if="tab === 'blueprint'"
              key="blueprint"
              :order="order"
              :tasks="tasks"
              :can-write="session.canWrite"
              :can-clarify="canClarify"
              :busy="busy"
              @draft="act(() => factory.planGenerate(id))"
              @approve="act(() => factory.planDecision(id, 'approve'))"
              @revise="open('revise')"
              @clarify="open('clarify')"
              @export="download" /><TaskGraph
              v-else-if="tab === 'graph'"
              key="graph"
              :tasks="tasks"
              :runs="runs" /><ActivityPanel
              v-else-if="tab === 'activity'"
              key="activity"
              :events="factory.events"
              :runs="runs" /><QualityPanel
              v-else-if="tab === 'quality'"
              key="quality"
              :checks="factory.quality"
              :findings="factory.findings"
              :revision="order.revision" /><ChangesPanel
              v-else
              key="changes"
              :runs="runs"
              :findings="factory.findings"
              :state="order.state"
              :can-write="session.canWrite"
              :busy="busy"
              :publish-to="
                project?.git_connection_id ? project.remote_repo : ''
              "
              :pr-url="order.pr_url"
              :pr-ref="order.pr_ref"
              :source-ref="order.source_ref"
              @decision="open"
          /></Transition>
        </div>
      </div>
      <WorkOrderSidebar :order="order" :project="project" :runs="runs" />
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
        This records your local approval for the current work order. It does not
        create, publish or merge a pull request.
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
