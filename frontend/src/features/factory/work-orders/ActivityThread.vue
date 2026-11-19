<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { ChevronRight, Download, FileText, Send } from "lucide-vue-next";
import type { Run, Task, WOEvent, WorkOrder } from "../../../types/api";
import ActorBadge from "../ActorBadge.vue";
import PanelCard from "../../projects/PanelCard.vue";
import { woPanelSlots } from "../../projects/woPanelSlots";
import BlueprintSection from "./BlueprintSection.vue";
import {
  groupActivity,
  liveRuns,
  liveVerb,
  type AgentRunGroup,
} from "../runs/agentActivity";
import {
  activityThread,
  handoffs,
  type ThreadItem,
} from "../runs/activityThread";
const props = withDefaults(
  defineProps<{
    order: WorkOrder;
    tasks: Task[];
    events: WOEvent[];
    runs: Run[];
    hasMore?: boolean;
    steer?: (runID: string, message: string) => Promise<unknown>;
  }>(),
  { hasMore: false, steer: undefined },
);
const emit = defineEmits<{ export: [] }>();

const items = computed(() =>
  activityThread(props.events, props.order, props.tasks, props.runs),
);
const groups = computed(() => groupActivity(props.events));
const live = computed(() => liveRuns(groups.value));

const filters = [
  { key: "all", label: "All" },
  { key: "tasks", label: "Tasks" },
  { key: "feedback", label: "Feedback" },
  { key: "you", label: "You" },
  { key: "system", label: "System" },
] as const;
type FilterKey = (typeof filters)[number]["key"];
const filter = ref<FilterKey>("all");
const matches = (it: ThreadItem, f: FilterKey) =>
  f === "all" ||
  (f === "tasks" && it.kind === "message" && it.type.startsWith("Task")) ||
  (f === "feedback" &&
    it.kind === "message" &&
    (it.type === "Feedback" || !!it.findings?.length)) ||
  (f === "you" && (it.from === "you" || it.to === "you")) ||
  (f === "system" && it.kind === "system");
const counts = computed(() =>
  Object.fromEntries(
    filters.map((f) => [
      f.key,
      items.value.filter((it) => it.kind !== "divider" && matches(it, f.key))
        .length,
    ]),
  ),
);
const shown = computed(() =>
  filter.value === "all"
    ? items.value
    : items.value.filter(
        (it) => it.kind !== "divider" && matches(it, filter.value),
      ),
);

const slot = computed(() => woPanelSlots.mid.value);
const pairs = computed(() => handoffs(items.value));
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const who = (a: string) => (a === "you" ? "You" : cap(a));
const time = (iso: string) =>
  new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const kindClass = (type: string) => type.split(/[ ·]/, 1)[0]!.toLowerCase();

const scroller = ref<HTMLElement | null>(null);
let stick = true;
const onScroll = () => {
  const el = scroller.value;
  if (el) stick = el.scrollHeight - el.scrollTop - el.clientHeight < 32;
};
const toBottom = async () => {
  await nextTick();
  if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight;
};
onMounted(toBottom);
watch(
  () => [shown.value.length, live.value.length],
  () => {
    if (stick) void toBottom();
  },
  { flush: "pre" },
);

const target = ref("");
const draft = ref("");
const sending = ref(false);
const sendError = ref("");
watch(
  live,
  (rs) => {
    if (!target.value) target.value = rs[0]?.id ?? "";
  },
  { immediate: true },
);
const picked = computed(() => live.value.find((r) => r.id === target.value));
const targetGone = computed(() => !!target.value && !picked.value);
const runLabel = (r: AgentRunGroup) => {
  const i = props.tasks.findIndex((t) => t.id === r.taskID);
  const task =
    i < 0
      ? ""
      : ` · #${String(i + 1).padStart(2, "0")} ${props.tasks[i]!.title}`;
  return cap(r.actor) + task;
};
const ta = ref<HTMLTextAreaElement | null>(null);
function resize() {
  const el = ta.value;
  if (!el) return;
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight, 200) + "px";
}
function keydown(e: KeyboardEvent) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    void send();
  }
}
const hint = computed(() =>
  targetGone.value
    ? "That run has finished. Pick another agent to steer."
    : picked.value
      ? "Enter to send · Shift+Enter for a new line"
      : "No agent is running right now, so there is no one to steer.",
);
async function send() {
  const message = draft.value.trim();
  const run = picked.value;
  if (!message || !run || !props.steer || sending.value) return;
  sending.value = true;
  sendError.value = "";
  try {
    await props.steer(run.id, message);
    draft.value = "";
    stick = true;
    void nextTick(resize);
  } catch (e) {
    sendError.value = e instanceof Error ? e.message : String(e);
  } finally {
    sending.value = false;
  }
}
</script>
<template>
  <div class="act" data-test="activity-thread">
    <section class="fx-card thread" aria-label="Work order conversation">
      <div class="thread-top">
        <div class="fx-chips" role="group" aria-label="Filter conversation">
          <button
            v-for="f in filters"
            :key="f.key"
            type="button"
            class="fx-chip"
            :class="{ on: filter === f.key }"
            :aria-pressed="filter === f.key"
            :data-test="'filter-' + f.key"
            @click="filter = f.key"
          >
            {{ f.label }} <b>{{ counts[f.key] }}</b>
          </button>
        </div>
        <button
          type="button"
          class="lg-btn small ghost"
          data-test="export-blueprint"
          @click="emit('export')"
        >
          <Download class="icon" />Export blueprint
        </button>
      </div>
      <div
        ref="scroller"
        class="msgs"
        role="log"
        aria-label="Conversation"
        :tabindex="0"
        @scroll.passive="onScroll"
      >
        <p v-if="hasMore" class="tiny muted" data-test="activity-more">
          Earlier events are not shown.
        </p>
        <template v-for="it in shown" :key="it.id">
          <div
            v-if="it.kind === 'divider'"
            class="divider"
            role="separator"
            :data-test="'divider-' + it.text.toLowerCase()"
          >
            <span class="tiny">{{ it.text }}</span>
          </div>
          <div v-else-if="it.kind === 'system'" class="sys" data-test="sys">
            <span class="fx-pill" :class="it.pill?.tone">{{
              it.pill?.label
            }}</span>
            <span class="txt">{{ it.text }}</span>
            <time class="tiny muted" :datetime="it.at">{{ time(it.at) }}</time>
          </div>
          <article
            v-else
            class="msg"
            :class="[
              { me: it.from === 'you' },
              kindClass(it.type),
              { feedback: it.type === 'Feedback' },
            ]"
            data-test="msg"
          >
            <ActorBadge :actor="it.from" class="av" />
            <div class="mbody">
              <div class="mh">
                <span class="who">{{ who(it.from) }}</span>
                <span class="to"
                  >→ <b>{{ who(it.to) }}</b></span
                >
                <span class="kind" :class="kindClass(it.type)">{{
                  it.type
                }}</span>
                <span v-if="it.pill" class="fx-pill" :class="it.pill.tone">{{
                  it.pill.label
                }}</span>
                <time :datetime="it.at">{{ time(it.at) }}</time>
              </div>
              <div class="bubble">
                <p v-for="(q, qi) in it.quotes" :key="qi" class="quote">
                  {{ q }}
                </p>
                <p v-if="it.text">{{ it.text }}</p>
                <ul v-if="it.findings?.length" class="findings">
                  <li
                    v-for="(f, i) in it.findings"
                    :key="i"
                    class="finding"
                    data-test="finding"
                  >
                    <span class="row wrap" style="gap: 8px">
                      <span class="sev" :class="f.severity">{{
                        f.severity
                      }}</span>
                      <strong class="small">{{ f.title }}</strong>
                    </span>
                    <span v-if="f.detail" class="tiny muted">{{
                      f.detail
                    }}</span>
                  </li>
                </ul>
                <details
                  v-for="d in it.docs"
                  :key="d.title"
                  class="doc"
                  data-test="doc"
                  :data-doc="d.section"
                >
                  <summary>
                    <span class="dico"><FileText class="icon" /></span>
                    <span class="dt"
                      ><strong>{{ d.title }}</strong></span
                    >
                    <ChevronRight class="icon chev" />
                  </summary>
                  <div class="body">
                    <BlueprintSection
                      :section="d.section"
                      :order="order"
                      :tasks="tasks"
                      :text="d.text"
                    />
                  </div>
                </details>
              </div>
            </div>
          </article>
        </template>
        <p v-if="!shown.length" class="empty">
          {{
            items.length > 1
              ? "Nothing matches this filter."
              : "The conversation starts when the crew picks this up."
          }}
        </p>
        <div
          v-for="r in live"
          :key="r.id"
          class="typing"
          role="status"
          data-test="typing"
        >
          <span class="sq" aria-hidden="true"
            ><i></i><i></i><i></i><i></i
          ></span>
          {{ liveVerb(r) }}
        </div>
      </div>
      <form
        v-if="steer"
        class="steer-dock"
        data-test="composer"
        @submit.prevent="send"
      >
        <div class="steer" :class="{ off: !picked }">
          <label class="sr-only" for="steer-msg">Message</label>
          <textarea
            id="steer-msg"
            ref="ta"
            v-model="draft"
            rows="1"
            :disabled="!picked || sending"
            :placeholder="
              picked
                ? `Steer ${cap(picked.actor)} while it works…`
                : 'Nothing to steer right now'
            "
            data-test="steer-input"
            @input="resize"
            @keydown="keydown"
          ></textarea>
          <div class="steer-bar">
            <template v-if="live.length || targetGone">
              <label class="sr-only" for="steer-to">Send to</label>
              <select id="steer-to" v-model="target" data-test="steer-to">
                <option v-for="r in live" :key="r.id" :value="r.id">
                  {{ runLabel(r) }}
                </option>
                <option v-if="targetGone" :value="target" disabled>
                  Run finished
                </option>
              </select>
            </template>
            <span class="steer-hint">{{ hint }}</span>
            <button
              class="steer-send"
              type="submit"
              aria-label="Send"
              :disabled="!picked || sending || !draft.trim()"
              data-test="steer-send"
            >
              <Send class="icon" />
            </button>
          </div>
        </div>
        <p v-if="sendError" class="form-error" role="alert">{{ sendError }}</p>
      </form>
    </section>
    <Teleport v-if="slot" :to="slot">
      <PanelCard id="handoffs" title="Handoffs &amp; loops">
        <div class="loop">
          <div v-for="h in pairs" :key="h.from + h.to" class="loop-row">
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
      </PanelCard>
    </Teleport>
  </div>
</template>
<style scoped>
.act {
  min-width: 0;
}
.thread {
  display: flex;
  flex-direction: column;
  padding: 8px 0 0;
  overflow: hidden;
}
.thread-top {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: wrap;
  padding: 14px 22px 10px;
}
.fx-chip b {
  font-variant-numeric: tabular-nums;
  opacity: 0.75;
}
.msgs {
  display: flex;
  flex-direction: column;
  gap: 18px;
  padding: 10px 22px 22px;
  max-height: min(760px, 70dvh);
  overflow-y: auto;
  overflow-x: hidden;
}
.msgs:focus-visible {
  outline: none;
  box-shadow: var(--shadow-focus);
}
.divider {
  display: flex;
  align-items: center;
  gap: 12px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.divider::before,
.divider::after {
  content: "";
  flex: 1;
  height: 1px;
  background: var(--line);
}
.msg {
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr);
  gap: 12px;
  max-width: 760px;
  min-width: 0;
}
.msg.me {
  margin-left: auto;
  grid-template-columns: minmax(0, 1fr) 28px;
}
.msg.me .av {
  order: 2;
}
.mbody {
  min-width: 0;
}
.mh {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 8px;
  font-size: 13px;
  margin-bottom: 6px;
}
.msg.me .mh {
  justify-content: flex-end;
}
.who {
  font-weight: 600;
}
.to {
  color: var(--muted);
}
.to b {
  color: var(--ink);
  font-weight: 500;
}
.mh time {
  color: var(--muted);
  font-size: 12px;
  margin-left: 2px;
  font-variant-numeric: tabular-nums;
}
.msg.me .mh time {
  margin-left: 0;
  order: -1;
  margin-right: 2px;
}
.kind {
  font: 500 10.5px/1 var(--font-mono);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  padding: 4px 7px;
  border-radius: 6px;
  background: var(--surface-2);
  color: var(--muted);
}
.kind.task {
  background: var(--info-soft);
  color: var(--info);
}
.kind.feedback {
  background: var(--warning-soft);
  color: var(--warning);
}
.kind.reply {
  background: var(--success-soft);
  color: var(--success);
}
.kind.steer {
  background: var(--brand);
  color: var(--on-brand);
}
.bubble {
  background: var(--surface-2);
  border-radius: 4px 14px 14px 14px;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
  overflow-wrap: anywhere;
}
.bubble p {
  margin: 0;
  white-space: pre-wrap;
}
.msg.feedback .bubble {
  background: var(--warning-soft);
}
.msg.me .bubble {
  background: var(--brand);
  color: var(--on-brand);
  border-radius: 14px 4px 14px 14px;
}
.findings {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 8px;
}
.finding {
  background: var(--surface);
  color: var(--ink);
  border-radius: 10px;
  padding: 10px 12px;
  display: grid;
  gap: 4px;
  min-width: 0;
}
.sev {
  font: 600 10.5px/1 var(--font-mono);
  text-transform: uppercase;
  padding: 3px 6px;
  border-radius: 5px;
  background: var(--surface-2);
  color: var(--muted);
}
.sev.blocker,
.sev.critical {
  background: var(--danger-soft);
  color: var(--danger);
}
.sev.major,
.sev.high {
  background: var(--warning-soft);
  color: var(--warning);
}
.quote {
  border-left: 2px solid var(--line-strong);
  padding: 2px 0 2px 10px;
  font-size: 12.5px;
  color: var(--muted);
}
.msg.me .quote {
  color: inherit;
  opacity: 0.85;
  border-color: currentColor;
}
.doc {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: 12px;
}
.doc summary {
  list-style: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  cursor: pointer;
  border-radius: 12px;
}
.doc summary::-webkit-details-marker {
  display: none;
}
.doc summary:focus-visible {
  outline: none;
  box-shadow: var(--shadow-focus);
}
.dico {
  width: 28px;
  height: 28px;
  border-radius: 8px;
  background: var(--tint);
  color: var(--brand);
  display: grid;
  place-items: center;
}
.dico .icon {
  width: 14px;
  height: 14px;
}
.dt {
  flex: 1;
  min-width: 0;
  font-size: 13.5px;
}
.chev {
  color: var(--muted);
  transition: transform 0.15s;
}
.doc[open] .chev {
  transform: rotate(90deg);
}
.doc .body {
  padding: 0 14px 14px;
  font-size: 13px;
  display: grid;
  gap: 10px;
  min-width: 0;
}
.sys {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
  color: var(--muted);
  padding-left: 40px;
  flex-wrap: wrap;
  min-width: 0;
}
.sys .txt {
  min-width: 0;
  overflow-wrap: anywhere;
}
.typing {
  display: flex;
  gap: 10px;
  align-items: center;
  padding-left: 40px;
  font-size: 13px;
  color: var(--muted);
}
.sq {
  display: inline-flex;
  gap: 3px;
}
.sq i {
  width: 7px;
  height: 7px;
  border-radius: 2px;
  background: var(--line-strong);
  animation: sq 1.4s infinite;
}
.sq i:nth-child(2) {
  animation-delay: 0.15s;
}
.sq i:nth-child(3) {
  animation-delay: 0.3s;
}
.sq i:nth-child(4) {
  animation-delay: 0.45s;
}
@keyframes sq {
  0%,
  100% {
    opacity: 0.25;
  }
  40% {
    opacity: 1;
  }
}
@media (prefers-reduced-motion: reduce) {
  .sq i {
    animation: none;
    opacity: 0.6;
  }
}
.steer-dock {
  display: grid;
  gap: var(--space-2);
  padding: var(--space-4) 22px 20px;
  border-top: 1px solid var(--line);
}
.steer {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-3) var(--space-3) var(--space-2) var(--space-4);
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--radius-xl);
  box-shadow: var(--shadow-card);
  transition:
    border-color var(--dur-fast),
    box-shadow var(--dur-fast);
}
.steer:focus-within {
  border-color: var(--accent);
  box-shadow: var(--shadow-focus);
}
.steer.off {
  background: var(--surface-2);
  box-shadow: none;
}
.steer textarea {
  width: 100%;
  min-height: 0;
  max-height: 200px;
  padding: 0;
  border: 0;
  outline: 0;
  resize: none;
  background: none;
  color: var(--ink);
  font: inherit;
  font-size: 14px;
  line-height: 1.5;
}
.steer textarea:disabled {
  opacity: 0.6;
}
.steer-bar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}
.steer-bar select {
  max-width: 45%;
  height: var(--control-height-sm);
  padding: 0 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius-full);
  background: var(--surface);
  color: var(--ink);
  font: inherit;
  font-size: 12.5px;
  text-overflow: ellipsis;
}
.steer-hint {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  color: var(--muted);
}
.steer-send {
  display: grid;
  place-items: center;
  width: var(--control-height-sm);
  height: var(--control-height-sm);
  margin-left: auto;
  padding: 0;
  border: 0;
  border-radius: var(--radius-full);
  background: var(--brand);
  color: var(--on-brand);
  cursor: pointer;
  transition:
    opacity var(--dur-fast),
    transform var(--dur-fast) var(--ease-out);
}
.steer-send:disabled {
  opacity: 0.35;
  cursor: default;
}
.steer-send:not(:disabled):active {
  transform: scale(0.92);
}
.steer-send:focus-visible {
  outline: none;
  box-shadow: var(--shadow-focus);
}
.steer-send .icon {
  width: 16px;
  height: 16px;
}
.steer-dock .form-error {
  margin: 0;
}
.loop {
  display: flex;
  flex-direction: column;
  gap: 10px;
  font-size: 13px;
}
.loop-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
@media (max-width: 720px) {
  .msgs,
  .thread-top,
  .steer-dock {
    padding-left: 14px;
    padding-right: 14px;
  }
  .sys,
  .typing {
    padding-left: 0;
  }
}
</style>
