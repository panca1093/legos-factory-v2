<script setup lang="ts">
import { computed } from "vue";
import { useFactory } from "../../stores/factory";

const props = defineProps<{
  idPrefix: string;
  label: string;
  repoUrl: string;
  connectionId: string;
  remoteRepo: string;
  contractTests?: boolean;
  disabled?: boolean;
}>();
const emit = defineEmits<{
  "update:connectionId": [string];
  "update:remoteRepo": [string];
  "update:contractTests": [boolean];
  commit: [];
}>();
const factory = useFactory();
const remoteShape = /^[\w.-]+(\/[\w.-]+)+$/;

const gitConnections = computed(() =>
  factory.connections.filter(
    (c) =>
      c.scope === "shared" && c.status === "active" && c.provider !== "jira",
  ),
);
const unavailable = computed(() => {
  const id = props.connectionId;
  if (!id || gitConnections.value.some((c) => c.id === id)) return "";
  return factory.connections.find((c) => c.id === id)?.name ?? id;
});

function pick(id: string) {
  emit("update:connectionId", id);
  let remote = props.remoteRepo;
  if (id && !remote && /^(https?:\/\/|git@)/.test(props.repoUrl)) {
    remote =
      props.repoUrl.match(/[:/]([\w.-]+\/[\w.-]+?)(?:\.git)?\/?$/)?.[1] ?? "";
    emit("update:remoteRepo", remote);
  }
  if (!id || remoteShape.test(remote.trim())) emit("commit");
}
function remoteChanged() {
  if (props.connectionId && remoteShape.test(props.remoteRepo.trim()))
    emit("commit");
}
</script>
<template>
  <div class="form-grid">
    <div class="form-group">
      <label :for="`${idPrefix}-conn`">Pull requests through</label>
      <select
        :id="`${idPrefix}-conn`"
        :aria-label="`Git connection for ${label}`"
        :value="connectionId"
        :disabled="disabled"
        @change="pick(($event.target as HTMLSelectElement).value)"
      >
        <option value="">None · gh login or local only</option>
        <option v-if="unavailable" :value="connectionId" disabled>
          {{ unavailable }} · unavailable
        </option>
        <option v-for="c in gitConnections" :key="c.id" :value="c.id">
          {{ c.name }} · {{ c.provider }}
        </option>
      </select>
    </div>
    <div v-if="connectionId" class="form-group">
      <label :for="`${idPrefix}-remote`">Remote repository</label>
      <input
        :id="`${idPrefix}-remote`"
        :aria-label="`Remote repository for ${label}`"
        :value="remoteRepo"
        :disabled="disabled"
        placeholder="owner/repo"
        @input="
          emit('update:remoteRepo', ($event.target as HTMLInputElement).value)
        "
        @change="remoteChanged"
      />
    </div>
    <p class="help" style="grid-column: 1 / -1">
      With a shared connection, an approved work order's verified commit is
      pushed to a branch and a pull request is opened. With none, the gh login
      on this computer is used for GitHub repositories; otherwise work stays
      local.
    </p>
    <label v-if="contractTests !== undefined" style="grid-column: 1 / -1">
      <input
        type="checkbox"
        :data-test="`contract-tests-${idPrefix}`"
        :checked="contractTests"
        :disabled="disabled"
        @change="
          emit(
            'update:contractTests',
            ($event.target as HTMLInputElement).checked,
          )
        "
      />
      Contract tests: write and lock tests for the acceptance criteria
    </label>
  </div>
</template>
