<script setup lang="ts">
import { computed } from "vue";
import AppDialog from "../../components/ui/AppDialog.vue";
import { useEditor } from "./editorStore";

const store = useEditor();
const names = computed(() =>
  (store.closeRequest?.paths ?? []).map((p) => p.split("/").pop()).join(", "),
);
const leaving = computed(() => store.dirtyPaths().length);
</script>

<template>
  <AppDialog
    :open="!!store.closeRequest"
    title="Unsaved changes"
    :subtitle="`${names} has unsaved changes.`"
    @close="store.answerClose('cancel')"
  >
    <p class="help">Save it before closing, or discard what you changed.</p>
    <template #footer>
      <button type="button" class="lg-btn" @click="store.answerClose('cancel')">
        Cancel
      </button>
      <button
        type="button"
        class="lg-btn danger"
        data-test="close-discard"
        @click="store.answerClose('discard')"
      >
        Don’t save
      </button>
      <button
        type="button"
        class="lg-btn primary"
        data-test="close-save"
        @click="store.answerClose('save')"
      >
        Save
      </button>
    </template>
  </AppDialog>
  <AppDialog
    :open="!!store.leavePrompt"
    title="Leave with unsaved changes?"
    :subtitle="`${leaving} file${leaving === 1 ? '' : 's'} in the editor ${leaving === 1 ? 'has' : 'have'} unsaved changes.`"
    @close="store.answerLeave('cancel')"
  >
    <p class="help">Save them all, or discard the changes.</p>
    <template #footer>
      <button type="button" class="lg-btn" @click="store.answerLeave('cancel')">
        Cancel
      </button>
      <button
        type="button"
        class="lg-btn danger"
        data-test="leave-discard"
        @click="store.answerLeave('discard')"
      >
        Discard
      </button>
      <button
        type="button"
        class="lg-btn primary"
        data-test="leave-save"
        @click="store.answerLeave('save')"
      >
        Save all
      </button>
    </template>
  </AppDialog>
</template>
