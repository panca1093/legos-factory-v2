<script setup lang="ts">
import { nextTick, ref, watch } from "vue";
import AppDialog from "../../../components/ui/AppDialog.vue";
const props = defineProps<{
  open: boolean;
  title: string;
  confirmLabel: string;
  busy?: boolean;
  blocked?: string;
  error?: string;
}>();
const emit = defineEmits<{ close: []; confirm: [] }>();
const cancel = ref<HTMLButtonElement>();
// AppDialog focuses its first control (the X) on open; Cancel must win for a destructive prompt.
watch(
  () => props.open,
  async (open) => {
    if (!open) return;
    await nextTick();
    cancel.value?.focus();
  },
  { immediate: true, flush: "post" },
);
</script>
<template>
  <AppDialog :open="open" :title="title" @close="!busy && emit('close')"
    ><div class="stack">
      <slot />
      <p v-if="blocked" class="form-error" role="alert">{{ blocked }}</p>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </div>
    <template #footer
      ><button
        ref="cancel"
        class="lg-btn"
        :disabled="busy"
        @click="emit('close')"
      >
        Cancel</button
      ><button
        class="lg-btn danger-solid"
        data-test="confirm-delete"
        :disabled="busy || !!blocked"
        @click="emit('confirm')"
      >
        {{ busy ? "Deleting…" : confirmLabel }}
      </button></template
    ></AppDialog
  >
</template>
