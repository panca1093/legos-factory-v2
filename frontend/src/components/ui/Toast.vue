<script setup lang="ts">
import { useToast } from "../../composables/useToast";
const { toasts, dismiss, pause, resume } = useToast();
</script>
<template>
  <div class="lg-toasts" aria-live="polite">
    <div
      v-for="t in toasts"
      :key="t.id"
      class="lg-toast"
      @mouseenter="pause(t.id)"
      @mouseleave="resume(t.id)"
      @focusin="pause(t.id)"
      @focusout="resume(t.id)"
    >
      <span>{{ t.message }}</span
      ><button
        v-if="t.undo"
        type="button"
        @click="
          t.undo();
          dismiss(t.id);
        "
      >
        Undo
      </button>
    </div>
  </div>
</template>
