<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import {
  ArrowUp,
  Check,
  ChevronDown,
  Paperclip,
  Sparkles,
  X,
} from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import Popover from "../../components/ui/Popover.vue";
import type { ChatModel } from "../../stores/chats";
import {
  MAX_MESSAGE_BYTES,
  addFiles,
  byteLength,
  formatSize,
  type PendingFile,
} from "./chatUtils";
const props = defineProps<{
  models: ChatModel[];
  busy?: boolean;
  readonly?: boolean;
}>();
const model = defineModel<string>("model", { default: "" });
const emit = defineEmits<{
  send: [payload: { content: string; files: PendingFile[] }];
  error: [message: string];
}>();
const draft = ref(""),
  files = ref<PendingFile[]>([]),
  dragging = ref(false),
  menuOpen = ref(false),
  ta = ref<HTMLTextAreaElement>(),
  picker = ref<HTMLInputElement>();
const bytes = computed(() => byteLength(draft.value));
const tooLong = computed(() => bytes.value > MAX_MESSAGE_BYTES);
const locked = computed(() => props.busy || props.readonly);
const canSend = computed(
  () => !locked.value && !tooLong.value && draft.value.trim().length > 0,
);
const modelName = computed(
  () => props.models.find((m) => m.id === model.value)?.name ?? "Auto",
);
function resize() {
  const el = ta.value;
  if (!el) return;
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight, 200) + "px";
}
function submit() {
  if (!canSend.value) return;
  emit("send", { content: draft.value.trim(), files: files.value });
  draft.value = "";
  files.value = [];
  void nextTick(resize);
}
function keydown(e: KeyboardEvent) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    submit();
  }
}
async function attach(list: FileList | File[] | null) {
  if (!list?.length || locked.value) return;
  const r = await addFiles(files.value, [...list]);
  files.value = r.files;
  if (r.errors.length) emit("error", r.errors.join(" "));
}
function drop(e: DragEvent) {
  dragging.value = false;
  void attach(e.dataTransfer?.files ?? null);
}
function onPick(e: Event) {
  const input = e.target as HTMLInputElement;
  void attach(input.files);
  input.value = "";
}
function restore(content: string, restored: PendingFile[]) {
  draft.value = content;
  files.value = restored;
  void nextTick(resize);
}
defineExpose({ restore, focus: () => ta.value?.focus() });
</script>
<template>
  <div
    class="composer"
    :class="{ drop: dragging }"
    @dragover.prevent="!locked && (dragging = true)"
    @dragleave.self="dragging = false"
    @drop.prevent="drop"
  >
    <div v-if="files.length" class="ch-chips">
      <span v-for="(f, i) in files" :key="f.name + i" class="ch-chip">
        <span class="ch-fn">{{ f.name }}</span
        ><span class="ch-fs">{{ formatSize(f.size) }}</span
        ><button
          type="button"
          :aria-label="`Remove ${f.name}`"
          @click="files.splice(i, 1)"
        >
          <X class="icon" />
        </button>
      </span>
    </div>
    <textarea
      ref="ta"
      v-model="draft"
      rows="1"
      aria-label="Message"
      :placeholder="readonly ? 'Viewers can read chats only.' : 'Ask anything…'"
      :disabled="locked"
      @input="resize"
      @keydown="keydown"
    ></textarea>
    <div class="ch-bar">
      <button
        type="button"
        class="lg-icon-btn ch-tool"
        aria-label="Attach files"
        title="Attach text files (or drop them here)"
        :disabled="locked"
        @click="picker?.click()"
      >
        <Paperclip class="icon" />
      </button>
      <input ref="picker" type="file" multiple hidden @change="onPick" />
      <span class="ch-hint" :class="{ over: tooLong }">{{
        tooLong
          ? `Message too long (${formatSize(bytes)} of 32 KB)`
          : "Enter to send · Shift+Enter for a new line"
      }}</span>
      <div class="ch-end">
        <Popover v-model:open="menuOpen" up>
          <template #trigger="{ toggle }">
            <Button
              variant="ghost"
              small
              aria-haspopup="menu"
              :aria-expanded="menuOpen"
              :disabled="locked"
              @click="toggle"
              ><Sparkles class="icon" />{{ modelName
              }}<ChevronDown class="icon"
            /></Button>
          </template>
          <button
            v-for="m in models"
            :key="m.id"
            type="button"
            :aria-pressed="m.id === model"
            @click="model = m.id"
          >
            <Check
              class="icon"
              :style="{ opacity: m.id === model ? 1 : 0 }"
            />{{ m.name }}
          </button>
        </Popover>
        <button
          type="button"
          class="ch-send"
          aria-label="Send"
          :disabled="!canSend"
          @click="submit"
        >
          <ArrowUp class="icon" />
        </button>
      </div>
    </div>
  </div>
</template>
