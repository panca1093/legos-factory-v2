<script setup lang="ts">
import { ref, watch } from "vue";
import { api, type LocalInspect, type Project } from "../../types/api";
import PanelCard from "./PanelCard.vue";
import ProjectChecks from "../factory/ProjectChecks.vue";
import { publishLabel } from "../../composables/presentation";
import { repoLabel } from "./rail";

const props = defineProps<{ project: Project }>();
const branch = ref("");
watch(
  () => props.project.id,
  async () => {
    const id = props.project.id;
    branch.value = "";
    try {
      const info = await api.get<LocalInspect>(
        `/api/v1/local/inspect?path=${encodeURIComponent(props.project.repo_url)}`,
      );
      if (id === props.project.id) branch.value = info.default_branch;
    } catch {
      /* Only available for local folders in a local workspace. */
    }
  },
  { immediate: true },
);
</script>
<template>
  <PanelCard id="details" title="Details">
    <dl class="pc-dl">
      <div>
        <dt>Repository</dt>
        <dd>{{ repoLabel(project) }}</dd>
      </div>
      <div>
        <dt>Default branch</dt>
        <dd>{{ branch || "—" }}</dd>
      </div>
      <div>
        <dt>Publishing</dt>
        <dd>{{ publishLabel(project) }}</dd>
      </div>
      <div v-if="project.language">
        <dt>Language</dt>
        <dd>{{ project.language }}</dd>
      </div>
    </dl>
    <h4 class="pc-sub">Project checks</h4>
    <ProjectChecks :project-id="project.id" />
  </PanelCard>
</template>
