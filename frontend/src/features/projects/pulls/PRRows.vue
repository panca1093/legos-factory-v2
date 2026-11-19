<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import {
  chipText,
  connectionsPath,
  pullPath,
  statusLine,
  type Identity,
  type PRRow,
  type ProviderErr,
} from "../../../types/review";

const props = defineProps<{
  projectId: string;
  rows: PRRow[];
  identity: Identity;
  error?: ProviderErr;
  stale?: boolean;
  empty?: string;
}>();
const noIdentity = computed(() => props.error?.code === "no_identity");
function ago(iso: string) {
  const m = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60000));
  if (!Number.isFinite(m)) return "";
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  return m < 1440
    ? `${Math.round(m / 60)}h ago`
    : `${Math.round(m / 1440)}d ago`;
}
const action = (r: PRRow) =>
  r.label === "none" || r.label === "yours" ? "Open" : "Review";
</script>
<template>
  <div class="prs">
    <p v-if="noIdentity" class="notice" data-test="pr-no-identity">
      {{ error?.message || "No review account is connected." }}
      <RouterLink class="prs-link" :to="connectionsPath"
        >Connect an account</RouterLink
      >
    </p>
    <template v-else>
      <p v-if="error" class="notice warn" role="alert" data-test="pr-error">
        {{ error.message }}
        <template v-if="rows.length || stale">
          Showing the last known list.</template
        >
      </p>
      <p v-if="!rows.length && !error" class="notice" data-test="pr-empty">
        {{ empty ?? "No open pull requests." }}
      </p>
      <ul v-if="rows.length" class="prs-list">
        <li v-for="r in rows" :key="r.number">
          <RouterLink
            :to="pullPath(projectId, r.number)"
            class="prs-row"
            :class="{ muted: r.label === 'none' }"
            data-test="pr-row"
          >
            <div class="prs-main">
              <div class="prs-title">
                <span class="mono muted tiny">#{{ r.number }}</span>
                <strong>{{ r.title }}</strong>
                <span v-if="r.draft" class="prs-draft" data-test="pr-draft"
                  >Draft</span
                >
              </div>
              <div class="prs-meta small muted">
                <span>{{ r.author.login }}</span>
                <span class="mono">{{ r.head }} → {{ r.base }}</span>
                <span>{{ ago(r.updated_at) }}</span>
                <span v-if="r.factory_wo_id" class="prs-factory">Factory</span>
              </div>
            </div>
            <span
              class="prs-status"
              :class="[r.label, { running: r.running }]"
              data-test="pr-status"
              >{{ statusLine(r) }}</span
            >
            <span v-if="chipText(r)" class="sr-only">{{ chipText(r) }}</span>
            <span class="prs-act">{{ action(r) }}</span>
          </RouterLink>
        </li>
      </ul>
    </template>
  </div>
</template>
