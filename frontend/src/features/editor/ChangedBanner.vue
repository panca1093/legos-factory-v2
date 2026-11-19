<script setup lang="ts">
import { computed } from "vue";
import type { Banner } from "./editorStore";

const props = defineProps<{ banner: Banner; name: string; dirty: boolean }>();
defineEmits<{
  reload: [];
  keep: [];
  compare: [];
  recreate: [];
  close: [];
}>();

const what = computed(() =>
  props.banner.kind === "deleted"
    ? "deleted on disk"
    : props.banner.kind === "conflict"
      ? "changed on disk, so it was not saved"
      : "changed on disk",
);
const role = computed(() => props.banner.actor?.role ?? "");
</script>

<template>
  <div
    class="cb"
    role="alert"
    data-test="changed-banner"
    :data-kind="banner.kind"
  >
    <span class="cb-text">
      <strong class="mono">{{ name }}</strong> {{ what }}
      <span
        v-if="banner.actor"
        class="cb-actor"
        :style="
          role
            ? { '--c': `var(--crew-${role}, var(--line-strong))` }
            : undefined
        "
        >{{ banner.actor.label }}</span
      >
    </span>
    <span class="cb-actions">
      <template v-if="banner.kind === 'deleted'">
        <button type="button" class="lg-btn small" @click="$emit('recreate')">
          Save to recreate
        </button>
        <button
          type="button"
          class="lg-btn small ghost"
          @click="$emit('close')"
        >
          Close tab
        </button>
      </template>
      <template v-else>
        <button
          type="button"
          class="lg-btn small"
          data-test="banner-reload"
          @click="$emit('reload')"
        >
          Reload
        </button>
        <button
          type="button"
          class="lg-btn small"
          data-test="banner-keep"
          @click="$emit('keep')"
        >
          Keep mine
        </button>
        <button
          type="button"
          class="lg-btn small"
          data-test="banner-compare"
          @click="$emit('compare')"
        >
          Compare
        </button>
      </template>
    </span>
  </div>
</template>

<style scoped>
.cb {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  padding: 8px 14px;
  border-bottom: 1px solid var(--line);
  background: var(--warning-soft);
  color: var(--warning);
  font-size: 13px;
}
.cb-actor {
  margin-left: 6px;
  padding: 1px 8px;
  border-radius: var(--radius-full);
  background: var(--c, var(--surface-2));
  color: var(--emerald-950);
  font-size: 11.5px;
  font-weight: 600;
}
.cb-actions {
  display: inline-flex;
  gap: var(--space-2);
}
</style>
