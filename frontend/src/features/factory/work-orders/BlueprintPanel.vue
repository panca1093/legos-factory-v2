<script setup lang="ts">
import { Download } from "lucide-vue-next";
import type { WorkOrder, Task } from "../../../types/api";
import BlueprintSection from "./BlueprintSection.vue";
defineProps<{ order: WorkOrder; tasks: Task[] }>();
const emit = defineEmits<{ export: [] }>();
</script>
<template>
  <div>
    <section
      v-if="order.clarif_answer"
      class="plan-section"
      data-test="decision"
    >
      <h3>Decision recorded</h3>
      <BlueprintSection section="decision" :order="order" :tasks="tasks" />
    </section>
    <section class="plan-section">
      <h3>The outcome</h3>
      <BlueprintSection section="outcome" :order="order" :tasks="tasks" />
    </section>
    <section v-if="order.brief" class="plan-section" data-test="brief">
      <h3>Foreman's brief</h3>
      <BlueprintSection section="brief" :order="order" :tasks="tasks" />
    </section>
    <section class="plan-section">
      <h3>Acceptance contract</h3>
      <BlueprintSection section="acceptance" :order="order" :tasks="tasks" />
    </section>
    <section v-if="order.spec" class="plan-section" data-test="spec">
      <h3>Architect's spec</h3>
      <BlueprintSection section="spec" :order="order" :tasks="tasks" />
    </section>
    <section class="plan-section">
      <h3>How the crew will build it</h3>
      <BlueprintSection section="tasks" :order="order" :tasks="tasks" />
    </section>
    <section class="plan-section">
      <h3>Scope boundaries</h3>
      <BlueprintSection section="scope" :order="order" :tasks="tasks" />
    </section>
    <button
      class="artifact-chip"
      aria-label="Download blueprint"
      @click="emit('export')"
    >
      <Download class="icon" />Download this blueprint as Markdown<span
        class="grow"
      ></span
      ><span class="muted">.md</span>
    </button>
  </div>
</template>
