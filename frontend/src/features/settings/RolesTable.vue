<script setup lang="ts">
import { onMounted, reactive, ref, watch } from "vue";
import { Lock } from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import type {
  AgentInfo,
  ModelInfo,
  RoleKey,
  Settings,
  SettingsPatch,
} from "../../types/api";

const props = defineProps<{
  settings: Settings;
  agents: AgentInfo[];
  editable: boolean;
}>();
const emit = defineEmits<{ saved: [SettingsPatch] }>();
const factory = useFactory();

const roles: { key: RoleKey; label: string; fixed?: string }[] = [
  { key: "foreman", label: "Foreman", fixed: "Read only" },
  { key: "architect", label: "Architect", fixed: "Read only" },
  { key: "builder", label: "Builder", fixed: "Edit its worktree" },
  { key: "inspector", label: "Inspector", fixed: "Read + run tests" },
  { key: "chat", label: "Chat" },
];
const modelField = (k: RoleKey) => `${k}_model` as const;
const pending = reactive<Partial<Record<RoleKey, string>>>({});
const agentOf = (k: RoleKey) =>
  pending[k] || props.settings.role_agents?.[k] || "claude";
const modelOf = (k: RoleKey) => props.settings[modelField(k)] ?? "";
const refOf = (m: ModelInfo) => `${m.provider_id}/${m.model_id}`;
const chatPerm = () => props.settings.role_permissions?.chat ?? "ask";

const byAgent = reactive<Record<string, ModelInfo[]>>({});
const busy = reactive(new Set<RoleKey>());
const error = ref("");
async function ensureModels(agent: string) {
  if (byAgent[agent]) return byAgent[agent];
  const models = await factory.loadModels(agent);
  if (models.length) byAgent[agent] = models;
  return models;
}
const load = () =>
  Promise.all(
    [...new Set(roles.map((r) => agentOf(r.key)))].map((a) =>
      ensureModels(a).catch((e) => {
        error.value = e instanceof Error ? e.message : String(e);
      }),
    ),
  );
onMounted(load);
watch(() => props.settings.role_agents, load);

const info = (id: string) => props.agents.find((a) => a.id === id);
const API_KEY_PLAN = "API key · ";
function serving(k: RoleKey) {
  const a = info(agentOf(k));
  return (
    a?.accounts.find((x) => (x.roles ?? []).includes(k)) ??
    a?.accounts.find((x) => x.active)
  );
}
function via(k: RoleKey) {
  const plan = serving(k)?.plan ?? "";
  return plan.startsWith(API_KEY_PLAN) ? plan.slice(API_KEY_PLAN.length) : "";
}
function hint(k: RoleKey) {
  const a = info(agentOf(k));
  if (!a) return "";
  if (!a.installed) return `${a.name} isn't installed`;
  if (!a.enabled) return `${a.name} is off`;
  const act = serving(k);
  if (!act || act.status === "signed_out") return `${a.name} isn't signed in`;
  return "";
}
const agentOptions = (k: RoleKey) =>
  props.agents.filter((a) => a.installed || a.id === agentOf(k));
function unsupportedNote(a: AgentInfo) {
  return a.roles_allowed.length === 1
    ? `${a.roles_allowed[0][0].toUpperCase()}${a.roles_allowed[0].slice(1)} only`
    : "not supported for this role";
}
const supported = (a: AgentInfo, k: RoleKey) => a.roles_allowed.includes(k);

let queue: Promise<unknown> = Promise.resolve();
function save(k: RoleKey, patch: SettingsPatch, el: HTMLSelectElement) {
  busy.add(k);
  const run = async () => {
    error.value = "";
    try {
      const saved = await factory.patchSettings(patch);
      emit("saved", {
        role_agents: saved.role_agents,
        role_permissions: saved.role_permissions,
        [modelField(k)]: saved[modelField(k)],
      });
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
      el.value = el.dataset.prev ?? "";
    } finally {
      delete pending[k];
      busy.delete(k);
    }
  };
  queue = queue.then(run, run);
  return queue;
}
async function changeAgent(k: RoleKey, el: HTMLSelectElement) {
  const agent = el.value;
  busy.add(k);
  error.value = "";
  let models: ModelInfo[] = [];
  try {
    models = await ensureModels(agent);
    if (!models.length) throw new Error(`No models found for ${agent}.`);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    el.value = el.dataset.prev ?? "";
    busy.delete(k);
    return;
  }
  pending[k] = agent;
  await save(
    k,
    {
      role_agents: { ...props.settings.role_agents, ...pending },
      [modelField(k)]: refOf(models[0]),
    },
    el,
  );
}
const changeModel = (k: RoleKey, el: HTMLSelectElement) =>
  save(k, { [modelField(k)]: el.value }, el);
const changePerm = (el: HTMLSelectElement) =>
  save("chat", { role_permissions: { chat: el.value as "ask" | "read" } }, el);
</script>
<template>
  <div class="table-wrap" data-test="roles">
    <table class="roles">
      <thead>
        <tr>
          <th scope="col">Role</th>
          <th scope="col">Agent</th>
          <th scope="col">Model</th>
          <th scope="col">Permission</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in roles" :key="r.key" :data-role="r.key">
          <th scope="row">{{ r.label }}</th>
          <td data-label="Agent">
            <select
              :id="`agent-${r.key}`"
              :aria-label="`${r.label} agent`"
              :data-prev="agentOf(r.key)"
              :value="agentOf(r.key)"
              :disabled="!editable || busy.has(r.key)"
              @change="changeAgent(r.key, $event.target as HTMLSelectElement)"
            >
              <option
                v-for="a in agentOptions(r.key)"
                :key="a.id"
                :value="a.id"
                :disabled="
                  a.id !== agentOf(r.key) &&
                  (!a.enabled || !supported(a, r.key))
                "
              >
                {{ a.name
                }}{{
                  !supported(a, r.key)
                    ? ` · ${unsupportedNote(a)}`
                    : !a.enabled
                      ? " · off"
                      : ""
                }}
              </option>
            </select>
            <p
              v-if="hint(r.key)"
              class="status bad"
              :data-test="`hint-${r.key}`"
            >
              {{ hint(r.key) }}
            </p>
          </td>
          <td data-label="Model">
            <select
              :id="`model-${r.key}`"
              :aria-label="`${r.label} model`"
              :data-prev="modelOf(r.key)"
              :value="modelOf(r.key)"
              :disabled="!editable || busy.has(r.key)"
              @change="changeModel(r.key, $event.target as HTMLSelectElement)"
            >
              <option v-if="!modelOf(r.key)" value="">Default</option>
              <option
                v-else-if="
                  !(byAgent[agentOf(r.key)] ?? []).some(
                    (m) => refOf(m) === modelOf(r.key),
                  )
                "
                :value="modelOf(r.key)"
              >
                {{ modelOf(r.key) }} · not currently available
              </option>
              <option
                v-for="m in byAgent[agentOf(r.key)] ?? []"
                :key="refOf(m)"
                :value="refOf(m)"
              >
                {{ m.name }}
              </option>
            </select>
            <p v-if="via(r.key)" class="help" :data-test="`via-${r.key}`">
              via {{ via(r.key) }}
            </p>
          </td>
          <td data-label="Permission">
            <select
              v-if="r.key === 'chat'"
              id="perm-chat"
              aria-label="Chat permission"
              :data-prev="chatPerm()"
              :value="chatPerm()"
              :disabled="!editable || busy.has('chat')"
              @change="changePerm($event.target as HTMLSelectElement)"
            >
              <option value="ask">Ask before tools</option>
              <option value="read">Read only</option>
            </select>
            <span v-else class="lock" title="Fixed for this role"
              ><Lock class="icon" aria-hidden="true" /> {{ r.fixed }}</span
            >
          </td>
        </tr>
      </tbody>
    </table>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
  </div>
</template>
