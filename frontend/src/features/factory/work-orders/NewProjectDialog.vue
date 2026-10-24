<script setup lang="ts">
import { reactive, ref } from "vue";
import AppDialog from "../../../components/ui/AppDialog.vue";
import { useFactory } from "../../../stores/factory";
import { useSession } from "../../../stores/session";
defineProps<{ open: boolean }>();
const emit = defineEmits<{ close: []; created: [string] }>();
const factory = useFactory(),
  session = useSession(),
  busy = ref(false),
  error = ref("");
const form = reactive({ name: "", repo_url: "", language: "go" });
async function create() {
  if (busy.value || !session.canWrite) return;
  error.value = "";
  if (!form.name.trim() || !form.repo_url.trim()) {
    error.value = "Project name and repository are required.";
    return;
  }
  busy.value = true;
  try {
    const p = await factory.createProject({
      ...form,
      name: form.name.trim(),
      repo_url: form.repo_url.trim(),
    });
    emit("created", p.id);
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
    title="A new home for your ideas."
    subtitle="Connect a repository to your factory"
    @close="!busy && emit('close')"
    ><form id="project-form" class="stack" @submit.prevent="create">
      <div class="form-group">
        <label for="prj-name">Project name</label
        ><input
          id="prj-name"
          v-model="form.name"
          required
          placeholder="Payments platform"
        />
      </div>
      <div class="form-group">
        <label for="prj-repo">Repository (local path or URL)</label
        ><input
          id="prj-repo"
          v-model="form.repo_url"
          required
          placeholder="/path/to/repo or https://…"
        />
      </div>
      <div class="form-group">
        <label for="prj-lang">Language / quality profile</label
        ><select id="prj-lang" v-model="form.language">
          <option value="go">Go</option>
          <option value="typescript">TypeScript / Vue</option>
          <option value="rust">Rust</option>
          <option value="node">Node</option>
        </select>
      </div>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer
      ><button class="btn" :disabled="busy" @click="emit('close')">
        Cancel</button
      ><button
        class="btn primary"
        form="project-form"
        :disabled="busy || !session.canWrite"
      >
        {{ busy ? "Creating…" : "Create project" }}
      </button></template
    ></AppDialog
  >
</template>
