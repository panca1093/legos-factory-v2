<script setup lang="ts">
import { ref, watch } from "vue";
import AppDialog from "../../components/ui/AppDialog.vue";
import { useFactory } from "../../stores/factory";
import type { CLILoginStep } from "../../types/reviewIdentity";

const props = defineProps<{ open: boolean; loginId: string; host: string }>();
const emit = defineEmits<{ close: []; finished: [] }>();
const factory = useFactory();

const code = ref(""),
  url = ref(""),
  state = ref<"waiting" | "done" | "failed" | "fallback">("waiting"),
  message = ref(""),
  copied = ref(false);
const seen = new Set<number>();

watch(
  () => props.loginId,
  () => {
    code.value = url.value = message.value = "";
    state.value = "waiting";
    copied.value = false;
    seen.clear();
  },
);
watch(
  () => factory.liveEvents,
  (events) => {
    if (!props.open || !props.loginId) return;
    for (const ev of [...events].reverse()) {
      if (!ev.kind.startsWith("cli_login.") || seen.has(ev.id)) continue;
      const step = ev.payload as CLILoginStep | undefined;
      if (step?.login_id !== props.loginId) continue;
      seen.add(ev.id);
      const kind = ev.kind.slice("cli_login.".length);
      if (kind === "code") code.value = step.code ?? "";
      else if (kind === "url") url.value = step.url ?? "";
      else if (kind === "done") {
        state.value = "done";
        emit("finished");
      } else if (kind === "failed" || kind === "fallback") {
        state.value = kind;
        message.value = step.message ?? "";
      }
    }
  },
  { deep: true, immediate: true },
);

async function copy() {
  try {
    await navigator.clipboard.writeText(code.value);
    copied.value = true;
  } catch {
    copied.value = false;
  }
}
</script>
<template>
  <AppDialog
    :open="open"
    title="Login with gh"
    :subtitle="host"
    @close="emit('close')"
  >
    <div v-if="state === 'done'" data-test="cli-done">
      <p>Signed in. Choose the account with "Use this".</p>
    </div>
    <div v-else-if="state === 'fallback'" data-test="cli-fallback">
      <p>
        Run <code>gh auth login -h {{ host }}</code> in a terminal, then
        Refresh.
      </p>
      <p v-if="message" class="help">{{ message }}</p>
    </div>
    <div v-else-if="state === 'failed'" data-test="cli-failed">
      <p class="form-error" role="alert">
        Login failed{{ message ? `: ${message}` : "." }}
      </p>
    </div>
    <div v-else data-test="cli-waiting">
      <p v-if="!code && !url" class="help" aria-busy="true">
        Waiting for gh to print a one-time code…
      </p>
      <div v-if="code" class="form-group">
        <label for="cli-code">One-time code</label>
        <div class="row">
          <code id="cli-code" data-test="cli-code">{{ code }}</code>
          <button class="btn small" type="button" @click="copy">
            {{ copied ? "Copied" : "Copy" }}
          </button>
        </div>
      </div>
      <p v-if="url">
        <a
          :href="url"
          target="_blank"
          rel="noopener noreferrer"
          data-test="cli-url"
          >Open {{ url }}</a
        >
        and enter the code to finish signing in.
      </p>
    </div>
    <div class="row">
      <button
        v-if="state === 'fallback'"
        class="btn primary small"
        type="button"
        @click="emit('finished')"
      >
        Refresh
      </button>
      <button class="btn small" type="button" @click="emit('close')">
        {{ state === "waiting" ? "Cancel" : "Close" }}
      </button>
    </div>
  </AppDialog>
</template>
