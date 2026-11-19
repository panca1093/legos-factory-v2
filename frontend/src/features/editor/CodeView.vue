<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { ArrowLeft } from "lucide-vue-next";
import EditorWorkbench from "./EditorWorkbench.vue";
import { useEditor } from "./editorStore";

const route = useRoute(),
  router = useRouter(),
  store = useEditor();
const projectId = computed(() => String(route.params.id));
const folderParam = computed(() => String(route.params.folder ?? ""));
const queryFile = computed(() => String(route.query.file ?? ""));
const queryLine = computed(() => Number(route.query.line) || 0);
const prHead = computed(() => String(route.query.pr_head ?? ""));
const project = computed(() => store.projects[projectId.value]);
const st = computed(() => store.folders[store.currentId]);
const active = computed(() => st.value?.active ?? "");
const dirty = computed(
  () => Object.values(st.value?.buffers ?? {}).filter((b) => b.dirty).length,
);
const activeDirty = computed(() => !!st.value?.buffers[active.value]?.dirty);
const failed = computed(
  () =>
    !folderParam.value &&
    !!project.value &&
    project.value.status !== "ready" &&
    project.value.status !== "loading",
);
const notOpen = computed(
  () =>
    !!folderParam.value &&
    store.currentId !== folderParam.value &&
    !!project.value &&
    project.value.status !== "loading" &&
    !st.value,
);

let seq = 0;
async function sync() {
  const mine = ++seq;
  const ok = await store.useFolder(projectId.value, folderParam.value);
  if (mine !== seq || !ok) return;
  if (queryFile.value)
    await store.openFile(store.currentId, queryFile.value, {
      line: queryLine.value || undefined,
    });
  else if (!st.value?.active && !st.value?.tabs.length) return;
  syncUrl();
}
watch([projectId, folderParam, queryFile, queryLine], () => void sync(), {
  immediate: true,
});

// The active tab is mirrored into ?file= with replace, so back keeps going to the previous selection.
function syncUrl() {
  const a = active.value;
  if (!a || a === queryFile.value) return;
  if (st.value?.status !== "ready") return;
  const query: Record<string, string | (string | null)[] | null> = {
    ...route.query,
    file: a,
  };
  delete query.line;
  void router.replace({ query });
}
watch(active, syncUrl);
onBeforeUnmount(() => seq++);

const backPath = computed(() => `/projects/${projectId.value}/code`);
const title = computed(() =>
  active.value ? active.value.split("/").pop() : "Code",
);
</script>

<template>
  <div class="cv">
    <div
      v-if="failed && project"
      class="panel empty"
      data-test="code-unavailable"
      role="status"
    >
      <h2>
        {{
          project.status === "unavailable"
            ? "The editor isn’t available here"
            : project.status === "needs_folder"
              ? "This project has no local folder"
              : "We couldn’t open this project’s folder"
        }}
      </h2>
      <p class="help">{{ project.message }}</p>
    </div>
    <div v-else-if="notOpen" class="panel empty" data-test="code-not-open">
      <h2>This folder isn’t open</h2>
      <p class="help">Open it from the work order’s Details card.</p>
      <RouterLink class="lg-btn section-gap" :to="backPath"
        >Back to project code</RouterLink
      >
    </div>
    <template v-else>
      <div class="cv-head">
        <div class="cv-title">
          <RouterLink v-if="folderParam" :to="backPath" class="cv-back small"
            ><ArrowLeft class="icon" />Back to project code</RouterLink
          >
          <h1>
            <template v-if="st?.folder.kind === 'worktree'"
              ><span class="mono">{{ st.folder.wo_id }}</span> worktree ·
            </template>
            <span class="mono">{{ title }}</span>
          </h1>
        </div>
        <div class="cv-actions">
          <button
            type="button"
            class="lg-btn small primary"
            data-test="save"
            :disabled="!activeDirty"
            @click="store.save(store.currentId, active)"
          >
            Save
          </button>
          <button
            v-if="dirty > 1"
            type="button"
            class="lg-btn small"
            data-test="save-all"
            @click="store.saveAll(store.currentId)"
          >
            Save all ({{ dirty }})
          </button>
        </div>
      </div>
      <EditorWorkbench :pr-head="prHead" />
    </template>
  </div>
</template>

<style scoped>
.cv {
  display: grid;
  gap: var(--space-3);
  min-width: 0;
}
.cv-head {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: var(--space-3);
  flex-wrap: wrap;
}
.cv-title h1 {
  margin: 4px 0 0;
  font: 600 22px var(--font-display);
}
.cv-back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--muted);
}
.cv-actions {
  display: flex;
  gap: var(--space-2);
}
</style>
