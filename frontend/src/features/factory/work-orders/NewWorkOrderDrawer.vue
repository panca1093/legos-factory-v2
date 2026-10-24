<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { ArrowLeft, ArrowRight, Check } from "lucide-vue-next";
import AppDialog from "../../../components/ui/AppDialog.vue";
import BrickBot from "../../../components/agents/BrickBot.vue";
import { useFactory } from "../../../stores/factory";
import { useSession } from "../../../stores/session";
import type { Settings } from "../../../types/api";
const props = defineProps<{ open: boolean; projectID?: string }>();
const emit = defineEmits<{
  close: [];
  created: [{ id: string; planningError: string }];
}>();
const factory = useFactory(),
  session = useSession(),
  step = ref(1),
  busy = ref(false),
  error = ref(""),
  config = ref<Settings | null>(null);
const form = reactive({
  project_id: "",
  title: "",
  objective: "",
  acceptance: "",
  autonomy: "assisted",
  builder_model: "",
  inspector_model: "",
});
const project = computed(() =>
  factory.projects.find((p) => p.id === form.project_id),
);
const allowed = computed(() =>
  factory.models.filter(
    (m) =>
      !config.value?.allowed_models ||
      config.value.allowed_models.includes(`${m.provider_id}/${m.model_id}`),
  ),
);
watch(
  () => props.open,
  (open) => {
    if (open) {
      step.value = 1;
      error.value = "";
      Object.assign(form, {
        project_id: props.projectID || factory.projects[0]?.id || "",
        title: "",
        objective: "",
        acceptance: "",
        autonomy: "assisted",
        builder_model: "",
        inspector_model: "",
      });
    }
  },
  { immediate: true },
);
watch(
  () => factory.projects,
  (projects) => {
    if (props.open && !form.project_id && projects.length)
      form.project_id = props.projectID || projects[0].id;
  },
  { immediate: true },
);
onMounted(async () => {
  try {
    await factory.refreshModels();
    config.value = await factory.loadSettings();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
function next() {
  error.value = "";
  if (!session.canWrite) return;
  if (step.value === 1) {
    if (!form.project_id || !form.title.trim() || !form.objective.trim()) {
      error.value = "Project, title and desired outcome are required.";
      return;
    }
    if (!form.acceptance.trim()) {
      error.value = "Observable acceptance criteria are required.";
      return;
    }
  }
  step.value++;
}
async function create() {
  if (busy.value || !session.canWrite) return;
  busy.value = true;
  error.value = "";
  try {
    const order = await factory.createWorkOrder({
      ...form,
      title: form.title.trim(),
      objective: form.objective.trim(),
      acceptance: form.acceptance.trim(),
    });
    let planningError = "";
    try {
      await factory.planGenerate(order.id);
    } catch (e) {
      planningError = e instanceof Error ? e.message : String(e);
    }
    emit("created", { id: order.id, planningError });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <AppDialog
    :open="open"
    title="Make something worthwhile."
    :subtitle="`New work order · Step ${step} of 3`"
    drawer
    @close="!busy && emit('close')"
  >
    <template #header-extra
      ><div class="stepper" :aria-label="`Step ${step} of 3`">
        <span v-for="i in 3" :key="i" :class="{ on: step >= i }"></span></div
    ></template>
    <div v-if="step === 1" class="stack">
      <div class="form-group">
        <label for="wo-title">What should the crew build?</label
        ><input
          id="wo-title"
          v-model="form.title"
          maxlength="200"
          placeholder="e.g. Make payment retries idempotent"
          required
        />
      </div>
      <div class="form-grid">
        <div class="form-group">
          <label for="wo-project">Project</label
          ><select id="wo-project" v-model="form.project_id">
            <option value="" disabled>Select a project</option>
            <option v-for="p in factory.projects" :key="p.id" :value="p.id">
              {{ p.name }}
            </option>
          </select>
        </div>
        <div class="form-group">
          <label for="wo-repo">Repository</label
          ><input
            id="wo-repo"
            :value="project?.repo_url || 'Choose a project'"
            readonly
          />
          <p class="help">
            Repository profile · {{ project?.language || "not selected" }}
          </p>
        </div>
      </div>
      <div class="form-group">
        <label for="wo-objective">Describe the outcome</label
        ><textarea
          id="wo-objective"
          v-model="form.objective"
          rows="4"
          placeholder="What problem should this solve? What should stay the same?"
          required
        ></textarea>
      </div>
      <div class="form-group">
        <label for="wo-acceptance">How will we know it works?</label
        ><textarea
          id="wo-acceptance"
          v-model="form.acceptance"
          rows="4"
          placeholder="Describe observable acceptance criteria…"
          required
        ></textarea>
        <p class="help">
          Specific behavior gives the crew a contract to verify.
        </p>
      </div>
    </div>
    <div v-else-if="step === 2" class="stack">
      <div>
        <h3>Choose the hands. Choose the eyes.</h3>
        <p class="help" style="margin-top: 6px">
          Task models can differ. Options come from your workspace catalog.
        </p>
      </div>
      <div class="form-grid">
        <div
          v-for="role in ['builder', 'inspector'] as const"
          :key="role"
          class="model-card"
        >
          <div class="agent-line">
            <BrickBot :persona="role" class="medium" />
            <div>
              <strong style="text-transform: capitalize">{{ role }}</strong>
              <p>
                {{
                  role === "builder"
                    ? "Implementation"
                    : "Code & testing review"
                }}
              </p>
            </div>
          </div>
          <label :for="`wo-${role}`" class="help">{{
            role === "builder"
              ? "Implementation model"
              : "Independent review model"
          }}</label
          ><select :id="`wo-${role}`" v-model="form[`${role}_model`]">
            <option value="">
              Workspace default{{
                config?.[`${role}_model`] ? " · " + config[`${role}_model`] : ""
              }}
            </option>
            <option
              v-for="m in allowed"
              :key="`${m.provider_id}/${m.model_id}`"
              :value="`${m.provider_id}/${m.model_id}`"
            >
              {{ m.name }} · {{ m.harness }}
            </option>
          </select>
        </div>
      </div>
      <div class="notice">
        <strong>Governance is inherited</strong>
        <p style="margin-top: 7px">
          Foreman: managed by backend<br />Architect:
          {{ config?.architect_model || "Workspace default" }}
        </p>
        <p style="margin-top: 7px">
          Governance snapshot details and personal overrides are not exposed by
          the API.
        </p>
      </div>
      <div class="form-group">
        <label for="wo-autonomy">Autonomy for this order</label
        ><select id="wo-autonomy" v-model="form.autonomy">
          <option value="guided">Guided</option>
          <option value="assisted">Assisted</option>
          <option value="delegated">Delegated</option>
        </select>
        <p class="help">
          Backend policy controls execution. PR decisions always stay human.
        </p>
      </div>
    </div>
    <div v-else class="stack">
      <div>
        <span class="pill purple">{{ project?.name }}</span>
        <h2 style="margin-top: 13px">{{ form.title }}</h2>
        <p class="muted small" style="margin-top: 9px; white-space: pre-wrap">
          {{ form.objective }}
        </p>
      </div>
      <div class="notice">
        <strong>Acceptance contract</strong>
        <p style="margin-top: 7px; white-space: pre-wrap">
          {{ form.acceptance }}
        </p>
      </div>
      <div class="form-grid">
        <div
          v-for="role in ['builder', 'inspector'] as const"
          :key="role"
          class="agent-line"
        >
          <BrickBot :persona="role" class="compact" />
          <div>
            <strong style="text-transform: capitalize">{{ role }}</strong>
            <p>
              {{
                factory.models.find(
                  (m) =>
                    `${m.provider_id}/${m.model_id}` === form[`${role}_model`],
                )?.name ||
                form[`${role}_model`] ||
                "Workspace default"
              }}
            </p>
          </div>
        </div>
      </div>
      <div class="readout">
        <div>
          <b style="font-size: 17px">Not reported</b
          ><span>cost estimate · not provided by backend</span>
        </div>
        <div>
          <b style="font-size: 17px; text-transform: capitalize">{{
            form.autonomy
          }}</b
          ><span>autonomy · {{ project?.language }} profile</span>
        </div>
      </div>
      <div class="notice">
        Creating saves a work order and starts blueprint planning. Architect
        reviews it, and unresolved decisions return to you. Execution harnesses
        are labeled on each run.
      </div>
    </div>
    <p v-if="error" class="form-error" role="alert" style="margin-top: 16px">
      {{ error }}
    </p>
    <template #footer
      ><button
        v-if="step > 1"
        class="btn"
        data-test="drawer-back"
        :disabled="busy"
        @click="step--"
      >
        <ArrowLeft class="icon" />Back</button
      ><button v-else class="btn" :disabled="busy" @click="emit('close')">
        Cancel</button
      ><span class="tiny muted">{{ step }} / 3</span
      ><button
        v-if="step < 3"
        class="btn primary"
        data-test="drawer-next"
        :disabled="!session.canWrite || busy"
        @click="next"
      >
        Continue<ArrowRight class="icon" /></button
      ><button
        v-else
        class="btn primary"
        data-test="drawer-create"
        :disabled="!session.canWrite || busy"
        @click="create"
      >
        <Check class="icon" />{{
          busy ? "Creating…" : "Create & draft blueprint"
        }}
      </button></template
    >
  </AppDialog>
</template>
