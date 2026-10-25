<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { AlertTriangle } from "lucide-vue-next";
import {
  duration,
  liveVerb,
  toolLine,
  type AgentRunGroup,
} from "./agentActivity";
const props = defineProps<{ run: AgentRunGroup }>();

const blocks = 8;
const now = ref(Date.now());
const pos = ref(0);
let dir = 1,
  scanTimer: ReturnType<typeof setInterval> | undefined,
  clockTimer: ReturnType<typeof setInterval> | undefined;

const stalled = computed(() => props.run.status === "stalled");
const verb = computed(() => [...liveVerb(props.run)]);
const recent = computed(() => props.run.steps.slice(-5));
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
      <template v-if="run.steps.length">Step {{ run.steps.length }} · </template
      >last activity {{ duration(run.lastAt, now) }} ago ·
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
      <template v-for="s in recent" :key="s.n"
        ><p v-for="(t, i) in s.tools" :key="`${s.n}-${i}`">
          {{ toolLine(t)
          }}<span v-if="t.failed" class="agent-failed"> · failed</span>
        </p></template
      >
    </div>
  </section>
</template>
