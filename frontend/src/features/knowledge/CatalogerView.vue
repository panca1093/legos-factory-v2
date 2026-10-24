<script setup lang="ts">
import { onMounted, ref } from "vue";
import { RouterLink } from "vue-router";
import { BookOpen, GitBranch, Search } from "lucide-vue-next";
import BrickBot from "../../components/agents/BrickBot.vue";
import AppDialog from "../../components/ui/AppDialog.vue";
import { api } from "../../services/api/client";
import { useFactory } from "../../stores/factory";
import type { KnowledgeRecord, Trust } from "../../types/api";

const factory = useFactory();
const records = ref<KnowledgeRecord[]>([]),
  projectId = ref(""),
  trust = ref<Trust | "">(""),
  text = ref(""),
  loading = ref(true),
  error = ref(""),
  selected = ref<KnowledgeRecord | null>(null);

const trustLabel: Record<Trust, { name: string; pill: string; help: string }> =
  {
    verified: {
      name: "Verified fact",
      pill: "green",
      help: "Recorded by the application from executed checks and workflow state.",
    },
    human_decision: {
      name: "Human decision",
      pill: "purple",
      help: "Made by a person: approvals, clarifications and PR dispositions.",
    },
    ai_interpretation: {
      name: "AI interpretation",
      pill: "orange",
      help: "Produced by an agent. Not authoritative; check its sources.",
    },
  };

function projectName(id: string) {
  return factory.projects.find((p) => p.id === id)?.name ?? id;
}

async function search() {
  loading.value = true;
  error.value = "";
  const q = new URLSearchParams();
  if (projectId.value) q.set("project_id", projectId.value);
  if (trust.value) q.set("trust", trust.value);
  if (text.value.trim()) q.set("q", text.value.trim());
  try {
    const res = await api.get<{ records: KnowledgeRecord[] }>(
      `/api/v1/knowledge${q.toString() ? `?${q}` : ""}`,
    );
    records.value = res.records ?? [];
  } catch (e) {
    records.value = [];
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}

onMounted(async () => {
  await Promise.allSettled([factory.refreshProjects()]);
  await search();
});

const shortSha = (sha?: string) => (sha ? sha.slice(0, 10) : "unversioned");
</script>
<template>
  <div>
    <section class="catalog-hero">
      <div>
        <span class="pill purple">Cataloger · Engineering knowledge</span>
        <h1>A factory that remembers why.</h1>
        <p>
          Decisions, verification results and change history are recorded as
          work orders progress, linked to their project, work order and commit.
          The Architect reads relevant records from the same project when it
          plans new work.
        </p>
      </div>
      <div class="catalog-illustration">
        <div class="floating-note">Decision + source</div>
        <BrickBot persona="cataloger" />
        <div class="floating-note">A little context, kept.</div>
      </div>
    </section>

    <form class="form-grid section-gap" role="search" @submit.prevent="search">
      <div class="form-group">
        <label for="kn-project">Project</label
        ><select id="kn-project" v-model="projectId" @change="search">
          <option value="">All projects you can access</option>
          <option v-for="p in factory.projects" :key="p.id" :value="p.id">
            {{ p.name }}
          </option>
        </select>
      </div>
      <div class="form-group">
        <label for="kn-trust">Trust</label
        ><select id="kn-trust" v-model="trust" @change="search">
          <option value="">Any</option>
          <option v-for="(t, key) in trustLabel" :key="key" :value="key">
            {{ t.name }}
          </option>
        </select>
      </div>
      <div class="form-group">
        <label for="kn-search">Keywords</label
        ><input
          id="kn-search"
          v-model="text"
          type="search"
          placeholder="e.g. idempotency, retries"
        />
      </div>
      <div class="form-group" style="align-self: end">
        <button class="btn primary" type="submit" data-test="knowledge-search">
          <Search class="icon" />Search
        </button>
      </div>
    </form>

    <p v-if="error" class="notice" role="alert">{{ error }}</p>
    <p v-else-if="loading" class="help" role="status">Loading knowledge…</p>
    <div v-else-if="!records.length" class="empty large section-gap">
      <h3>No knowledge recorded yet.</h3>
      <p style="margin-top: 8px">
        Records appear here as work orders are planned, verified and decided.
      </p>
    </div>
    <div v-else class="catalog-grid section-gap">
      <button
        v-for="k in records"
        :key="k.id"
        class="memory-card"
        :data-trust="k.trust"
        @click="selected = k"
      >
        <span class="pill" :class="trustLabel[k.trust].pill">{{
          trustLabel[k.trust].name
        }}</span>
        <h3 style="margin: 16px 0 8px">{{ k.title }}</h3>
        <p class="small muted" style="line-height: 1.6">
          {{ k.body.length > 160 ? k.body.slice(0, 160) + "…" : k.body }}
        </p>
        <p class="help" style="margin-top: 16px">
          {{ projectName(k.project_id) }} · {{ k.kind.replace(/_/g, " ") }} ·
          <GitBranch class="icon" />{{ shortSha(k.commit_sha) }}
        </p>
      </button>
    </div>

    <div class="resource-strip">
      <BookOpen class="icon" /><strong>Sources before certainty.</strong
      ><span
        >AI interpretations are labelled and never treated as decisions. Secrets
        are redacted and raw logs are not stored.</span
      >
    </div>

    <AppDialog
      :open="!!selected"
      :title="selected?.title ?? 'Knowledge record'"
      @close="selected = null"
    >
      <template v-if="selected">
        <p>
          <span class="pill" :class="trustLabel[selected.trust].pill">{{
            trustLabel[selected.trust].name
          }}</span>
        </p>
        <p class="help">{{ trustLabel[selected.trust].help }}</p>
        <pre class="knowledge-body" style="white-space: pre-wrap">{{
          selected.body
        }}</pre>
        <dl class="small">
          <dt>Project</dt>
          <dd>{{ projectName(selected.project_id) }}</dd>
          <template v-if="selected.work_order_id">
            <dt>Work order</dt>
            <dd>
              <RouterLink
                :to="{
                  name: 'work-order',
                  params: { id: selected.work_order_id },
                }"
                @click="selected = null"
                >{{ selected.work_order_id }}</RouterLink
              >
            </dd>
          </template>
          <dt>Commit</dt>
          <dd>
            <code>{{ selected.commit_sha || "unversioned" }}</code>
          </dd>
          <template v-if="selected.repo_url">
            <dt>Repository</dt>
            <dd>
              <code>{{ selected.repo_url }}</code>
            </dd>
          </template>
          <dt>Recorded by</dt>
          <dd>
            {{ selected.actor }} ·
            {{ new Date(selected.created_at).toLocaleString() }}
          </dd>
          <template v-if="selected.source_refs.length">
            <dt>Sources</dt>
            <dd>
              <ul>
                <li v-for="r in selected.source_refs" :key="r">
                  <code>{{ r }}</code>
                </li>
              </ul>
            </dd>
          </template>
        </dl>
      </template>
    </AppDialog>
  </div>
</template>
