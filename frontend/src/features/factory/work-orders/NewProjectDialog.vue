<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import AppDialog from "../../../components/ui/AppDialog.vue";
import { useFactory } from "../../../stores/factory";
import { useSession } from "../../../stores/session";
const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{ close: []; created: [string] }>();
const factory = useFactory(),
  session = useSession(),
  busy = ref(false),
  error = ref("");
const form = reactive({
  name: "",
  repo_url: "",
  language: "go",
  git_connection_id: "",
  remote_repo: "",
});
const createdID = ref("");
const gitConnections = computed(() =>
  factory.connections.filter(
    (c) =>
      c.scope === "shared" && c.status === "active" && c.provider !== "jira",
  ),
);
watch(
  () => props.open,
  (open) => {
    if (!open) return;
    createdID.value = "";
    error.value = "";
    factory.refreshConnections().catch(() => {});
  },
  { immediate: true },
);
async function create() {
  if (busy.value || !session.canWrite) return;
  if (createdID.value) {
    emit("created", createdID.value);
    return;
  }
  error.value = "";
  if (!form.name.trim() || !form.repo_url.trim()) {
    error.value = "Project name and repository are required.";
    return;
  }
  if (
    form.git_connection_id &&
    !/^[\w.-]+(\/[\w.-]+)+$/.test(form.remote_repo.trim())
  ) {
    error.value = "Remote repository must look like owner/repo.";
    return;
  }
  busy.value = true;
  try {
    const p = await factory.createProject({
      name: form.name.trim(),
      repo_url: form.repo_url.trim(),
      language: form.language,
    });
    if (form.git_connection_id) {
      try {
        await factory.bindProject(p.id, {
          git_connection_id: form.git_connection_id,
          remote_repo: form.remote_repo.trim(),
        });
      } catch (e) {
        createdID.value = p.id;
        error.value = `Project created without a repository connection: ${
          e instanceof Error ? e.message : String(e)
        }`;
        return;
      }
    }
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
      <div class="form-group">
        <label for="prj-conn">Pull requests (optional)</label
        ><select id="prj-conn" v-model="form.git_connection_id">
          <option value="">Local only · no pull requests</option>
          <option v-for="c in gitConnections" :key="c.id" :value="c.id">
            {{ c.name }} · {{ c.provider }}
          </option>
        </select>
        <p class="help">
          Shared connections only. After a human approves a work order, its
          verified commit is pushed to a branch and a pull request is opened.
        </p>
      </div>
      <div v-if="form.git_connection_id" class="form-group">
        <label for="prj-remote">Remote repository</label
        ><input
          id="prj-remote"
          v-model="form.remote_repo"
          required
          placeholder="owner/repo"
        />
        <p class="help">
          The repository above must be its clone URL, e.g.
          https://github.com/owner/repo.git.
        </p>
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
        {{ busy ? "Creating…" : createdID ? "Close" : "Create project" }}
      </button></template
    ></AppDialog
  >
</template>
