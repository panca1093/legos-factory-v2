<script setup lang="ts">
import { Settings, ShieldCheck } from "lucide-vue-next";
import type { WorkOrder, Project, Run } from "../../../types/api";
import BrickBot from "../../../components/agents/BrickBot.vue";
import { money } from "../../../composables/presentation";
defineProps<{ order: WorkOrder; project?: Project; runs: Run[] }>();
</script>
<template>
  <aside class="detail-side" aria-label="Work order configuration">
    <section class="side-section">
      <div class="side-label">Your crew for this order</div>
      <div
        v-for="role in [
          'foreman',
          'architect',
          'builder',
          'inspector',
        ] as const"
        :key="role"
        class="agent-line"
      >
        <BrickBot :persona="role" class="compact" />
        <div>
          <strong style="text-transform: capitalize">{{ role }}</strong>
          <p>
            {{ order[`${role}_model`] || "Workspace default" }}
          </p>
        </div>
      </div>
      <button
        class="btn text small"
        disabled
        style="margin-top: 10px; padding-left: 0"
      >
        <Settings class="icon" />Task model selection
      </button>
      <p class="help">Editing existing assignments is unavailable.</p>
    </section>
    <section class="side-section">
      <div class="side-label">Autonomy</div>
      <strong class="small" style="text-transform: capitalize">{{
        order.autonomy
      }}</strong>
      <p class="help" style="margin-top: 5px">
        Execution follows backend policy and evidence gates.
      </p>
    </section>
    <section class="side-section">
      <div class="side-label">Recorded usage</div>
      <div class="row between">
        <strong>{{
          money(runs.reduce((sum, r) => sum + r.cost_micros, 0))
        }}</strong
        ><span class="tiny muted">{{ runs.length }} runs</span>
      </div>
      <p class="help" style="margin-top: 10px">
        Harness accounting; estimates retain their run labels. Per-order
        estimates are not reported.
      </p>
    </section>
    <section class="side-section">
      <div class="side-label">Repository quality profile</div>
      <p class="small">{{ project?.language || "Repository" }}</p>
      <p class="help" style="margin: 5px 0 10px">
        Acceptance checks · revision-specific evidence · Inspector review
      </p>
      <span class="tiny status-line"
        ><ShieldCheck class="icon" />Inspect commands in Quality</span
      >
    </section>
  </aside>
</template>
