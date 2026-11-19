<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import "../factory/factory.css";
import "./pulls/pulls.css";
import { fetchInbox, usePRRefresh } from "../../stores/reviews";
import { prFilter, type InboxGroup, type PRFilter } from "../../types/review";
import PRFilterChips from "./pulls/PRFilterChips.vue";
import PRRows from "./pulls/PRRows.vue";

const route = useRoute(),
  router = useRouter();
const filter = computed(() => prFilter(route.query.filter));
const groups = ref<InboxGroup[] | null>(null),
  failed = ref("");
let seq = 0;
async function load() {
  const mine = ++seq;
  try {
    const res = await fetchInbox(filter.value);
    if (mine !== seq) return;
    groups.value = res ?? [];
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
  filter,
  () => {
    groups.value = null;
    void load();
  },
  { immediate: true },
);
usePRRefresh(() => void load());
onBeforeUnmount(() => seq++);
</script>
<template>
  <div class="fx-page">
    <div class="page-heading">
      <div>
        <div class="eyebrow">Across every project</div>
        <h1>Reviews</h1>
      </div>
    </div>
    <PRFilterChips :model-value="filter" @update:model-value="setFilter" />
    <p
      v-if="failed && !groups"
      class="notice warn"
      role="alert"
      data-test="reviews-failed"
    >
      Could not load reviews. {{ failed }}
      <button type="button" class="lg-btn small" @click="load">
        Try again
      </button>
    </p>
    <p v-else-if="!groups" class="notice" data-test="reviews-loading">
      Loading…
    </p>
    <p v-else-if="!groups.length" class="notice" data-test="reviews-empty">
      No projects yet.
    </p>
    <section
      v-for="g in groups ?? []"
      :key="g.project.id"
      class="prs-group"
      data-test="review-group"
    >
      <h2>
        <RouterLink :to="`/projects/${g.project.id}/pulls`">{{
          g.project.name
        }}</RouterLink>
      </h2>
      <PRRows
        :project-id="g.project.id"
        :rows="g.rows ?? []"
        :identity="g.identity"
        :error="g.error"
      />
    </section>
  </div>
</template>
