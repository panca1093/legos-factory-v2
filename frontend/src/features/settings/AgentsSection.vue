<script setup lang="ts">
import { computed, ref } from "vue";
import { useFactory } from "../../stores/factory";
import type { Settings } from "../../types/api";
import BrickBot from "../../components/agents/BrickBot.vue";

const props = defineProps<{ settings: Settings; editable: boolean }>();
const emit = defineEmits<{ saved: [Settings] }>();
const factory = useFactory();
const busy = ref(""),
  error = ref("");

type Key = "foreman" | "architect" | "builder" | "inspector" | "chat";
const rows: { key: Key; label: string }[] = [
  { key: "foreman", label: "Foreman" },
  { key: "architect", label: "Architect" },
  { key: "builder", label: "Builder" },
  { key: "inspector", label: "Inspector" },
  { key: "chat", label: "Chat" },
];
const field = (k: Key) => `${k}_model` as const;
const catalog = computed(() =>
  factory.models.map((m) => ({
    id: `${m.provider_id}/${m.model_id}`,
    name: m.name,
  })),
);
const nameOf = (ref: string) =>
  catalog.value.find((m) => m.id === ref)?.name ?? ref;
const firstAvailable = computed(() => catalog.value[0]?.name ?? "");
function fallback(k: Key) {
  if (k !== "chat") return firstAvailable.value;
  return props.settings.architect_model
    ? nameOf(props.settings.architect_model)
    : firstAvailable.value;
}
function resolved(k: Key) {
  return fallback(k) || "first available model";
}
const value = (k: Key) => props.settings[field(k)] ?? "";

async function change(k: Key, el: HTMLSelectElement) {
  const previous = value(k);
  busy.value = k;
  error.value = "";
  try {
    emit("saved", await factory.patchSettings({ [field(k)]: el.value }));
  } catch (e) {
    el.value = previous;
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = "";
  }
}
</script>
<template>
  <section id="agents" class="settings-section">
    <h2>Agents</h2>
    <p>
      The model each role uses. Order of choice: work order choice → workspace
      default → first available model. Foreman and Architect models are fixed
      per work order when it is created; changes apply to new orders.
    </p>
    <div class="form-grid">
      <div v-for="r in rows" :key="r.key" class="form-group">
        <label :for="`set-${r.key}`" class="row"
          ><BrickBot
            v-if="r.key !== 'chat'"
            :persona="r.key"
            class="compact"
          /><span>{{ r.label }}</span></label
        ><select
          :id="`set-${r.key}`"
          :value="value(r.key)"
          :disabled="!editable || busy === r.key"
          @change="change(r.key, $event.target as HTMLSelectElement)"
        >
          <option value="">Default ({{ resolved(r.key) }})</option>
          <option
            v-if="value(r.key) && !catalog.some((m) => m.id === value(r.key))"
            :value="value(r.key)"
          >
            {{ value(r.key) }} · not currently available
          </option>
          <option v-for="m in catalog" :key="m.id" :value="m.id">
            {{ m.name }}
          </option>
        </select>
        <p v-if="r.key === 'chat'" class="help">
          Used when a chat is on Auto. Left on default, it follows Architect, or
          Builder in project chats.
        </p>
      </div>
    </div>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
  </section>
</template>
