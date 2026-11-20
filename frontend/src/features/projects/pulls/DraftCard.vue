<script setup lang="ts">
import { computed, ref } from "vue";
import { useReviews } from "../../../stores/reviews";
import { useToast } from "../../../composables/useToast";
import { renderMarkdown } from "../../knowledge/notes";
import type { PRDraft } from "../../../types/review";

const props = defineProps<{ draft: PRDraft }>();
const store = useReviews(),
  { toast } = useToast();
const editing = ref(false),
  text = ref(""),
  busy = ref(false),
  open = ref(false);
const d = computed(() => props.draft);
const collapsed = computed(
  () =>
    d.value.status === "discarded" ||
    d.value.status === "superseded" ||
    d.value.status === "outdated",
);
const own = computed(() => d.value.run_id === "");
const edited = computed(() => !own.value && d.value.body !== d.value.ai_body);
const html = computed(() => renderMarkdown(d.value.body));
const stateNote = computed(
  () =>
    ({
      discarded: own.value ? "Deleted" : "Discarded",
      superseded: "Superseded by a newer run",
      outdated: "Outdated: the file changed since this draft",
    })[d.value.status as string] ?? "",
);
const place = computed(() =>
  d.value.inline ? `${d.value.path}:${d.value.line}` : "General",
);
async function act(patch: { status?: string; body?: string }) {
  busy.value = true;
  try {
    await store.patchDraft(d.value.id, patch);
    editing.value = false;
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  } finally {
    busy.value = false;
  }
}
function startEdit() {
  text.value = d.value.body;
  editing.value = true;
}
</script>
<template>
  <article
    class="dc"
    :class="[d.severity, d.status]"
    :data-test="`draft-${d.id}`"
    :data-status="d.status"
  >
    <template v-if="collapsed && !open">
      <div class="dc-row">
        <span class="dc-note"
          >{{ stateNote }} · {{ own ? "your comment" : d.title }}</span
        >
        <button
          v-if="d.status === 'discarded'"
          type="button"
          class="lg-btn small ghost"
          data-test="draft-undo"
          :disabled="busy"
          @click="act({ status: own ? 'accepted' : 'pending' })"
        >
          Undo
        </button>
        <button
          v-else
          type="button"
          class="lg-btn small ghost"
          @click="open = true"
        >
          Show
        </button>
      </div>
    </template>
    <template v-else>
      <header class="dc-head">
        <template v-if="own">
          <span class="dc-you" data-test="draft-own">You</span>
          <strong class="dc-title">Your comment</strong>
        </template>
        <template v-else>
          <span class="dc-sev" :data-sev="d.severity">{{ d.severity }}</span>
          <strong class="dc-title">{{ d.title }}</strong>
        </template>
        <span v-if="edited" class="dc-edited" data-test="draft-edited"
          >edited</span
        >
        <span v-if="d.status === 'accepted' && !own" class="dc-ok"
          >accepted</span
        >
        <span v-if="d.status === 'posted'" class="dc-ok">posted</span>
      </header>
      <p class="dc-place mono tiny muted">{{ place }}</p>
      <textarea
        v-if="editing"
        v-model="text"
        class="dc-edit"
        rows="5"
        aria-label="Edit comment"
        data-test="draft-text"
      ></textarea>
      <!-- eslint-disable-next-line vue/no-v-html -->
      <div v-else class="dc-body md" v-html="html"></div>
      <p v-if="d.post_error" class="dc-err" role="alert">{{ d.post_error }}</p>
      <div v-if="editing" class="dc-actions">
        <button
          type="button"
          class="lg-btn small primary"
          data-test="draft-save"
          :disabled="busy || !text.trim()"
          @click="act({ body: text })"
        >
          Save
        </button>
        <button
          type="button"
          class="lg-btn small ghost"
          @click="editing = false"
        >
          Cancel
        </button>
      </div>
      <div v-else-if="d.status === 'pending'" class="dc-actions">
        <button
          type="button"
          class="lg-btn small primary"
          data-test="draft-accept"
          :disabled="busy"
          @click="act({ status: 'accepted' })"
        >
          Accept
        </button>
        <button
          type="button"
          class="lg-btn small"
          data-test="draft-edit"
          @click="startEdit"
        >
          Edit
        </button>
        <button
          type="button"
          class="lg-btn small ghost"
          data-test="draft-discard"
          :disabled="busy"
          @click="act({ status: 'discarded' })"
        >
          Discard
        </button>
      </div>
      <div v-else-if="d.status === 'accepted'" class="dc-actions">
        <button
          type="button"
          class="lg-btn small"
          data-test="draft-edit"
          @click="startEdit"
        >
          Edit
        </button>
        <button
          v-if="own"
          type="button"
          class="lg-btn small ghost"
          data-test="draft-delete"
          :disabled="busy"
          @click="act({ status: 'discarded' })"
        >
          Delete
        </button>
        <button
          v-else
          type="button"
          class="lg-btn small ghost"
          data-test="draft-unaccept"
          :disabled="busy"
          @click="act({ status: 'pending' })"
        >
          Un-accept
        </button>
      </div>
      <div v-else-if="collapsed" class="dc-actions">
        <button type="button" class="lg-btn small ghost" @click="open = false">
          Hide
        </button>
      </div>
    </template>
  </article>
</template>
