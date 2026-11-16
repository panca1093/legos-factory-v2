<script setup lang="ts">
import { ref, watch } from "vue";
import { useFactory } from "../../stores/factory";
import type {
  RuntimeInfo,
  Settings,
  SettingsPatch,
} from "../../types/api";

const props = defineProps<{
  settings: Settings;
  runtime: RuntimeInfo | null;
  editable: boolean;
}>();
const emit = defineEmits<{ saved: [SettingsPatch] }>();
const factory = useFactory();

const runs = ref(props.settings.max_concurrent_runs ?? 4);
watch(
  () => props.settings.max_concurrent_runs,
  (v) => {
    if (v) runs.value = v;
  },
);
const busy = ref(false),
  error = ref(""),
  keyOpen = ref(false),
  key = ref("");

async function save(patch: SettingsPatch) {
  busy.value = true;
  error.value = "";
  try {
    const saved = await factory.patchSettings(patch);
    emit("saved", {
      max_concurrent_runs: saved.max_concurrent_runs,
      jev_enabled: saved.jev_enabled,
    });
    return true;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    runs.value = props.settings.max_concurrent_runs ?? runs.value;
    return false;
  } finally {
    busy.value = false;
  }
}
function saveRuns() {
  const n = Math.trunc(Number(runs.value));
  if (!Number.isFinite(n) || n < 1 || n > 16) {
    error.value = "Enter a number from 1 to 16.";
    runs.value = props.settings.max_concurrent_runs ?? 4;
    return;
  }
  runs.value = n;
  if (n !== props.settings.max_concurrent_runs)
    void save({ max_concurrent_runs: n });
}
async function toggleJev(el: HTMLInputElement) {
  if (!(await save({ jev_enabled: el.checked }))) el.checked = !el.checked;
}
async function saveKey() {
  if (!key.value.trim()) return;
  if (await save({ jev_key: key.value.trim() })) {
    key.value = "";
    keyOpen.value = false;
  }
}
</script>
<template>
  <section id="advanced" class="settings-section">
    <h2>Advanced</h2>
    <p>Workspace limits. Only admins can change these.</p>

    <div class="switch-row">
      <div>
        <label for="max-runs"><b>Agents working at once</b></label>
        <p class="help">
          How many agents (Builders, Inspectors, Foreman…) can work in parallel
          on this machine. Extra ones wait in line. Higher finishes work orders
          faster but uses more CPU, memory and subscription quota.
        </p>
      </div>
      <input
        id="max-runs"
        v-model.number="runs"
        class="num"
        type="number"
        min="1"
        max="16"
        :disabled="!editable || busy"
        @change="saveRuns"
      />
    </div>

    <div class="switch-row">
      <div>
        <b>Jev plan checks</b>
        <span class="status" data-test="jev-status">{{
          settings.jev_enabled ? "On" : "Off · every plan waits for you"
        }}</span>
        <p class="help">
          Plans that pass the checks start building without your click. Off
          means every plan waits for you.
        </p>
      </div>
      <label class="switch"
        ><input
          type="checkbox"
          aria-label="Jev plan checks"
          :checked="!!settings.jev_enabled"
          :disabled="!editable || busy"
          @change="toggleJev($event.target as HTMLInputElement)" /><span></span
      ></label>
    </div>

    <div class="form-group jev-key">
      <template v-if="runtime?.jev_key_from_env">
        <label for="jev-key">Jev key</label>
        <input id="jev-key" disabled placeholder="Set by the server" />
        <p class="help">Set by the server.</p>
      </template>
      <template v-else-if="keyOpen">
        <label for="jev-key">Jev key</label>
        <input
          id="jev-key"
          v-model="key"
          type="password"
          autocomplete="new-password"
          placeholder="Paste the key"
          :disabled="!editable || busy"
        />
        <div class="row">
          <button
            class="btn primary small"
            type="button"
            :disabled="!editable || busy || !key.trim()"
            @click="saveKey"
          >
            Save key
          </button>
          <button class="btn small" type="button" @click="keyOpen = false">
            Cancel
          </button>
        </div>
        <p class="help">Stored encrypted and never shown again.</p>
      </template>
      <button
        v-else
        class="btn small"
        type="button"
        :disabled="!editable"
        @click="keyOpen = true"
      >
        Change key
      </button>
    </div>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
  </section>
</template>
