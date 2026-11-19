<script setup lang="ts">
import { computed } from "vue";
import { crew } from "../../composables/presentation";
import { useFactory } from "../../stores/factory";
import PanelCard from "./PanelCard.vue";

const factory = useFactory();
const order = computed(() => factory.detail?.work_order);
const runs = computed(() => factory.detail?.runs ?? []);
const roles = computed(() =>
  crew
    .filter((c) => c.id !== "cataloger")
    .map((c) => ({
      ...c,
      active: runs.value.some((r) => r.kind === c.id && r.state === "running"),
    })),
);
const date = (iso: string) =>
  new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(iso));
</script>
<template>
  <template v-if="order">
    <PanelCard id="crew" title="Crew">
      <ul class="pc-crew">
        <li v-for="c in roles" :key="c.id" :data-role="c.id">
          <i
            class="pc-swatch"
            :style="{ background: `var(--crew-${c.id})` }"
          ></i>
          <span class="pc-name">{{ c.name }}</span>
          <span class="pc-state" :class="{ on: c.active }">{{
            c.active ? "active" : "idle"
          }}</span>
        </li>
      </ul>
    </PanelCard>
    <PanelCard id="wo-details" title="Details">
      <dl class="pc-dl">
        <div>
          <dt>Branch</dt>
          <dd class="mono">legos/{{ order.id }}</dd>
        </div>
        <div>
          <dt>Base branch</dt>
          <dd class="mono">{{ order.base_branch || "default" }}</dd>
        </div>
        <div>
          <dt>Started</dt>
          <dd>{{ date(order.created_at) }}</dd>
        </div>
        <div v-if="order.pr_url">
          <dt>Pull request</dt>
          <dd>
            <a :href="order.pr_url" target="_blank" rel="noopener noreferrer">{{
              order.pr_ref || "Open on host"
            }}</a>
          </dd>
        </div>
      </dl>
      <button
        type="button"
        class="lg-btn small"
        disabled
        title="The editor is not available yet"
      >
        Open in Code
      </button>
    </PanelCard>
  </template>
</template>
