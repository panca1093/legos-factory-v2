<script setup lang="ts">
import { useToast } from "../../composables/useToast";
const { toasts, dismiss } = useToast();
</script>
<template>
  <div class="lg-toasts" aria-live="polite">
    <div v-for="t in toasts" :key="t.id" class="lg-toast" role="status">
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
