<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { AlertTriangle, Send } from "lucide-vue-next";
import {
  duration,
  liveVerb,
  toolLine,
  type AgentRunGroup,
} from "./agentActivity";
const props = defineProps<{
  run: AgentRunGroup;
  task?: string;
  steer?: (message: string) => Promise<unknown>;
}>();
const draft = ref(""),
  sending = ref(false),
  steerError = ref("");
async function send() {
  const message = draft.value.trim();
  if (!message || !props.steer || sending.value) return;
  sending.value = true;
  steerError.value = "";
  try {
    await props.steer(message);
    draft.value = "";
  } catch (e) {
    steerError.value = e instanceof Error ? e.message : String(e);
  } finally {
    sending.value = false;
  }
}

const blocks = 8;
const now = ref(Date.now());
const pos = ref(0);
let dir = 1,
  scanTimer: ReturnType<typeof setInterval> | undefined,
  clockTimer: ReturnType<typeof setInterval> | undefined;

const stalled = computed(() => props.run.status === "stalled");
const verb = computed(() => [...liveVerb(props.run)]);
const recent = computed(() => props.run.steps.slice(-5));
const stepCount = computed(
  () => props.run.steps.filter((s) => !s.message).length,
);
const earlier = computed(() => props.run.steps.length - recent.value.length);

// Trail fades behind the head in the direction it is moving.
function opacity(i: number) {
  const d = dir > 0 ? pos.value - i : i - pos.value;
  return [1, 0.6, 0.35, 0.2][d] ?? 0.12;
}
function scan() {
  clearInterval(scanTimer);
  if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
  scanTimer = setInterval(
    () => {
      pos.value += dir;
      if (pos.value >= blocks - 1 || pos.value <= 0) dir *= -1;
    },
    stalled.value ? 450 : 90,
  );
}
onMounted(() => {
  scan();
  clockTimer = setInterval(() => (now.value = Date.now()), 1000);
});
watch(stalled, scan);
onBeforeUnmount(() => {
  clearInterval(scanTimer);
  clearInterval(clockTimer);
});
</script>
<template>
  <section
    class="agent-live"
    :class="{ stalled }"
    role="status"
    data-test="agent-live"
  >
    <div class="agent-live-head">
      <span class="agent-scan" aria-hidden="true"
        ><i v-for="i in blocks" :key="i" :style="{ opacity: opacity(i - 1) }"
      /></span>
      <strong class="agent-verb"
        ><span
          v-for="(ch, i) in verb"
          :key="i"
          :style="{ animationDelay: `${i * 0.05}s` }"
          >{{ ch }}</span
        ></strong
      >
    </div>
    <p class="tiny muted agent-live-meta">
      <template v-if="task">{{ task }} · </template
      ><template v-if="stepCount">Step {{ stepCount }} · </template>last
      activity {{ duration(run.lastAt, now) }} ago ·
      {{ duration(run.startedAt, now)
      }}<template v-if="run.tokensOut">
        · {{ run.tokensOut.toLocaleString() }} tokens</template
      >
    </p>
    <p v-if="stalled" class="agent-stall" data-test="agent-stalled">
      <AlertTriangle class="icon" />No new step for
      {{ duration(run.lastAt, now) }}. The model may be stuck; the run times out
      after 15m.
    </p>
    <div v-if="run.steps.length" class="agent-transcript">
      <p v-if="earlier" class="tiny muted">
        {{ earlier }} earlier step{{ earlier === 1 ? "" : "s" }}
      </p>
      <template v-for="s in recent" :key="`${s.at}-${s.n}`"
        ><p v-if="s.message" class="agent-steer">You: {{ s.message }}</p>
        <p v-for="(t, i) in s.tools" :key="`${s.n}-${i}`">
          {{ toolLine(t)
          }}<span v-if="t.failed" class="agent-failed"> · failed</span>
        </p></template
      >
    </div>
    <form v-if="steer" class="agent-steer-form" @submit.prevent="send">
      <input
        v-model="draft"
        data-test="steer-input"
        :disabled="sending"
        placeholder="Tell the agent something while it works…"
        aria-label="Message to the running agent"
      /><button
        class="btn small"
        data-test="steer-send"
        :disabled="sending || !draft.trim()"
      >
        <Send class="icon" />Send
      </button>
    </form>
    <p v-if="steerError" class="tiny agent-failed" role="alert">
      {{ steerError }}
    </p>
  </section>
</template>
