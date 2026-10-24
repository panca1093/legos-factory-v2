<script setup lang="ts">
import { ref } from "vue";
import { BookOpen, FileText, GitBranch, Sparkles } from "lucide-vue-next";
import BrickBot from "../../components/agents/BrickBot.vue";
import AppDialog from "../../components/ui/AppDialog.vue";
const enabled = ref(false),
  selected = ref<string | null>(null);
const categories = [
  {
    name: "Decisions, with context",
    type: "Decision",
    icon: FileText,
    text: "Keep the why, the source and the person who made the call.",
  },
  {
    name: "Patterns worth keeping",
    type: "Pattern",
    icon: Sparkles,
    text: "Turn reviewed approaches into something the next project can learn from.",
  },
  {
    name: "Changes with a story",
    type: "Change record",
    icon: GitBranch,
    text: "Connect the original intent to the evidence and final human decision.",
  },
];
</script>
<template>
  <div>
    <section class="catalog-hero">
      <div>
        <span class="pill purple">Cataloger · Future / experimental</span>
        <h1>A factory that remembers why.</h1>
        <p>
          A possible second brain for decisions, patterns, and change
          history—with sources you can trace. This is an optional concept
          preview.
        </p>
        <label
          class="switch-row"
          style="border: 0; padding-bottom: 0; max-width: 340px"
          ><span class="small">Explore sample knowledge records</span
          ><span class="switch"
            ><input
              v-model="enabled"
              data-test="knowledge-preview"
              type="checkbox"
              aria-label="Explore sample knowledge records" /><span></span></span
        ></label>
      </div>
      <div class="catalog-illustration">
        <div class="floating-note">Decision + source</div>
        <BrickBot persona="cataloger" />
        <div class="floating-note">A little context, kept.</div>
      </div>
    </section>
    <div v-if="enabled">
      <div class="notice">
        UI-only preview. Nothing is ingested, retrieved or promoted into memory.
        These cards describe a possible future capability.
      </div>
      <div class="catalog-grid section-gap">
        <button
          v-for="c in categories"
          :key="c.name"
          class="memory-card"
          @click="selected = c.name"
        >
          <span class="pill purple"
            ><component :is="c.icon" class="icon" />{{ c.type }}</span
          >
          <h3 style="margin: 20px 0 10px">{{ c.name }}</h3>
          <p class="small muted" style="line-height: 1.8">{{ c.text }}</p>
          <p class="help" style="margin-top: 24px">
            Concept preview · no stored records
          </p>
        </button>
      </div>
    </div>
    <div v-else class="empty large">
      <h3>Keep the idea optional.</h3>
      <p style="margin-top: 8px">
        Explore what a source-linked knowledge space could become.
      </p>
    </div>
    <div class="resource-strip">
      <BookOpen class="icon" /><strong>Sources before certainty.</strong
      ><span
        >Ingestion, permissions, freshness and curation are future work.</span
      >
    </div>
    <AppDialog
      :open="!!selected"
      :title="selected ?? 'Knowledge preview'"
      @close="selected = null"
      ><div class="notice">
        UI-only preview. A future record would retain its source work order,
        revision, scope and human curation status. No record is created or
        collected.
      </div></AppDialog
    >
  </div>
</template>
