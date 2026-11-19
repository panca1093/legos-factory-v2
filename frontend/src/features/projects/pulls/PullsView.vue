<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import "../../factory/factory.css";
import "./pulls.css";
import { fetchPulls, eventTarget, usePRRefresh } from "../../../stores/reviews";
import { useFactory } from "../../../stores/factory";
import {
  prFilter,
  type PRFilter,
  type PullsResponse,
} from "../../../types/review";
import PRFilterChips from "./PRFilterChips.vue";
import PRRows from "./PRRows.vue";

const route = useRoute(),
  router = useRouter(),
  factory = useFactory();
const projectId = computed(() => String(route.params.id));
const filter = computed(() => prFilter(route.query.filter));
const project = computed(() =>
  factory.projects.find((p) => p.id === projectId.value),
);
const data = ref<PullsResponse | null>(null),
  failed = ref("");
let seq = 0;
async function load() {
  const mine = ++seq;
  try {
    const res = await fetchPulls(projectId.value, filter.value);
    if (mine !== seq) return;
    data.value = res;
    failed.value = "";
  } catch (e) {
    if (mine === seq) failed.value = e instanceof Error ? e.message : String(e);
  }
}
function setFilter(f: PRFilter) {
  void router.replace({
    query: { ...route.query, filter: f === "related" ? undefined : f },
  });
}
watch(
  [projectId, filter],
  () => {
    data.value = null;
    void load();
  },
  { immediate: true },
);
usePRRefresh(
  () => void load(),
  (ev) => {
    const t = eventTarget(ev);
    return !t.project || t.project === projectId.value;
  },
);
onBeforeUnmount(() => seq++);
const empty = computed(
  () =>
    ({
      related: "No open pull requests involve you.",
      reviewer: "Nobody has asked you to review a pull request.",
      author: "You have no open pull requests.",
    })[filter.value],
);
</script>
<template>
  <div class="fx-page">
    <div class="page-heading">
      <div>
        <div class="eyebrow">Pull requests</div>
        <h1>{{ project?.name ?? "Pull requests" }}</h1>
      </div>
    </div>
    <PRFilterChips :model-value="filter" @update:model-value="setFilter" />
    <p
      v-if="failed && !data"
      class="notice warn"
      role="alert"
      data-test="pulls-failed"
    >
      Could not load pull requests. {{ failed }}
      <button type="button" class="lg-btn small" @click="load">
        Try again
      </button>
    </p>
    <p v-else-if="!data" class="notice" data-test="pulls-loading">Loading…</p>
    <PRRows
      v-else
      :project-id="projectId"
      :rows="data.rows ?? []"
      :identity="data.identity"
      :error="data.error"
      :stale="data.stale"
      :empty="empty"
    />
  </div>
</template>
