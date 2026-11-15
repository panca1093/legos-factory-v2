<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import {
  AlertCircle,
  ArrowUp,
  Check,
  ChevronDown,
  FileText,
  Paperclip,
  Sparkles,
  X,
} from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import Popover from "../../components/ui/Popover.vue";
import type { ChatModel } from "../../stores/chats";
import { deleteChatFile, uploadChatFile } from "../../services/api/chatFiles";
import {
  MAX_MESSAGE_BYTES,
  byteLength,
  fileSig,
  formatSize,
  precheckFile,
  type ChatUpload,
} from "./chatUtils";
const props = defineProps<{
  models: ChatModel[];
  busy?: boolean;
  readonly?: boolean;
  chatId?: string;
  ensureChat?: () => Promise<string>;
  dragging?: boolean;
}>();
const model = defineModel<string>("model", { default: "" });
const emit = defineEmits<{
  send: [payload: { content: string; files: ChatUpload[] }];
  error: [message: string];
}>();
const draft = ref(""),
  files = ref<ChatUpload[]>([]),
  menuOpen = ref(false),
  ta = ref<HTMLTextAreaElement>(),
  picker = ref<HTMLInputElement>();
const ctrls = new Map<string, AbortController>();
let uploadChat = "";
let seq = 0;
const bytes = computed(() => byteLength(draft.value));
const tooLong = computed(() => bytes.value > MAX_MESSAGE_BYTES);
const locked = computed(() => props.busy || props.readonly);
const failed = computed(() => files.value.find((f) => f.status === "error"));
const uploading = computed(() =>
  files.value.filter((f) => f.status === "uploading"),
);
const fileBlock = computed(() =>
  failed.value
    ? `Upload failed for ${failed.value.name}. Remove it to send.`
    : uploading.value.length
      ? uploading.value.length === 1
        ? `Uploading ${uploading.value[0].name}…`
        : `Uploading ${uploading.value.length} files…`
      : "",
);
const canSend = computed(
  () =>
    !locked.value &&
    !tooLong.value &&
    !fileBlock.value &&
    draft.value.trim().length > 0,
);
const modelName = computed(
  () => props.models.find((m) => m.id === model.value)?.name ?? "Auto",
);
const makePreview = (f: File) =>
  typeof URL.createObjectURL === "function"
    ? URL.createObjectURL(f)
    : undefined;
const revoke = (u: ChatUpload) => {
  if (u.preview && typeof URL.revokeObjectURL === "function")
    URL.revokeObjectURL(u.preview);
};
function resize() {
  const el = ta.value;
  if (!el) return;
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight, 200) + "px";
}
function submit() {
  if (!canSend.value) return;
  emit("send", {
    content: draft.value.trim(),
    files: files.value.map((f) => ({ ...f, preview: undefined })),
  });
  files.value.forEach(revoke);
  ctrls.clear();
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
const slot = (key: string) => files.value.find((f) => f.key === key);
function discard(u: ChatUpload) {
  ctrls.get(u.key)?.abort();
  ctrls.delete(u.key);
  if (u.id && u.status === "done")
    void deleteChatFile(uploadChat, u.id).catch(() => undefined);
  revoke(u);
}
function remove(key: string) {
  const u = slot(key);
  if (!u) return;
  discard(u);
  files.value = files.value.filter((f) => f.key !== key);
}
function discardAll() {
  files.value.forEach(discard);
  files.value = [];
}
function start(key: string, chat: string) {
  const u = slot(key);
  if (!u) return;
  const ac = new AbortController();
  ctrls.set(key, ac);
  uploadChatFile(
    chat,
    u.file,
    (p) => {
      const cur = slot(key);
      if (cur) cur.progress = p;
    },
    ac.signal,
  ).then(
    (info) => {
      const cur = slot(key);
      if (!cur || ctrls.get(key) !== ac) {
        void deleteChatFile(chat, info.id).catch(() => undefined);
        return;
      }
      ctrls.delete(key);
      Object.assign(cur, {
        id: info.id,
        name: info.name,
        size: info.size,
        kind: info.kind,
        status: "done",
        progress: 1,
      });
    },
    (e: unknown) => {
      const cur = slot(key);
      if (!cur || ctrls.get(key) !== ac) return;
      ctrls.delete(key);
      Object.assign(cur, {
        status: "error",
        error: e instanceof Error ? e.message : "Upload failed.",
      });
    },
  );
}
async function attach(list: FileList | File[] | null) {
  if (!list?.length || locked.value) return;
  const errors: string[] = [];
  const accepted: ChatUpload[] = [];
  const counted = () => [
    ...files.value.filter((f) => f.status !== "error"),
    ...accepted,
  ];
  for (const file of list) {
    const sig = fileSig(file);
    if ([...files.value, ...accepted].some((f) => fileSig(f.file) === sig)) {
      errors.push(`${file.name} is already attached.`);
      continue;
    }
    const r = precheckFile(file, counted());
    if ("error" in r) {
      errors.push(r.error);
      continue;
    }
    accepted.push({
      key: `u${++seq}`,
      name: file.name,
      size: file.size,
      kind: r.kind,
      status: "uploading",
      progress: 0,
      preview: r.kind === "image" ? makePreview(file) : undefined,
      file,
    });
  }
  if (errors.length) emit("error", [...new Set(errors)].join(" "));
  if (!accepted.length) return;
  let chat = props.chatId || "";
  if (!chat) {
    try {
      chat = (await props.ensureChat?.()) ?? "";
    } catch (e) {
      accepted.forEach(revoke);
      emit("error", e instanceof Error ? e.message : String(e));
      return;
    }
  }
  if (!chat || (props.chatId && props.chatId !== chat)) {
    accepted.forEach(revoke);
    return;
  }
  uploadChat = chat;
  files.value = [...files.value, ...accepted];
  for (const u of accepted) start(u.key, chat);
}
function onPaste(e: ClipboardEvent) {
  const data = e.clipboardData;
  if (!data || data.getData("text/plain")) return;
  const images = [...data.files].filter((f) => f.type.startsWith("image/"));
  if (!images.length) return;
  e.preventDefault();
  const stamp = Date.now();
  void attach(
    images.map(
      (f, i) =>
        new File(
          [f],
          `pasted-${stamp}-${i + 1}.${f.type.split("/")[1]?.replace("jpeg", "jpg") || "png"}`,
          { type: f.type, lastModified: stamp },
        ),
    ),
  );
}
function onPick(e: Event) {
  const input = e.target as HTMLInputElement;
  void attach(input.files ? [...input.files] : null);
  input.value = "";
}
function restore(owner: string, content: string, restored: ChatUpload[]) {
  if (owner !== (props.chatId || "")) return;
  if (!draft.value.trim()) draft.value = content;
  if (restored.length && !files.value.length) {
    uploadChat = owner;
    files.value = restored.map((f) => ({
      ...f,
      preview: f.kind === "image" ? makePreview(f.file) : undefined,
    }));
  }
  void nextTick(resize);
}
watch(
  () => props.chatId,
  (id) => {
    if (files.value.length && id !== uploadChat) discardAll();
  },
);
onBeforeUnmount(discardAll);
defineExpose({ restore, attach, focus: () => ta.value?.focus() });
</script>
<template>
  <div class="composer" :class="{ drop: dragging }">
    <div v-if="files.length" class="ch-chips">
      <span
        v-for="f in files"
        :key="f.key"
        class="ch-chip"
        :class="{ up: f.status === 'uploading', err: f.status === 'error' }"
        :data-status="f.status"
      >
        <img v-if="f.preview" :src="f.preview" alt="" class="ch-thumb" />
        <AlertCircle v-else-if="f.status === 'error'" class="icon" />
        <FileText v-else class="icon" />
        <span class="ch-fn">{{ f.name }}</span>
        <span v-if="f.status === 'uploading'" class="ch-fs"
          >{{ Math.round(f.progress * 100) }}%</span
        >
        <span v-else-if="f.status === 'error'" class="ch-fs" role="alert">{{
          f.error
        }}</span>
        <span v-else class="ch-fs">{{ formatSize(f.size) }}</span>
        <span
          v-if="f.status === 'uploading'"
          class="ch-bar-fill"
          role="progressbar"
          :aria-label="`Uploading ${f.name}`"
          :aria-valuenow="Math.round(f.progress * 100)"
          aria-valuemin="0"
          aria-valuemax="100"
          :style="{ width: `${f.progress * 100}%` }"
        ></span>
        <button
          type="button"
          :aria-label="`Remove ${f.name}`"
          @click="remove(f.key)"
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
      @paste="onPaste"
    ></textarea>
    <div class="ch-bar">
      <button
        type="button"
        class="lg-icon-btn ch-tool"
        aria-label="Attach files"
        title="Attach images, PDFs or text files (or drop them here)"
        :disabled="locked"
        @click="picker?.click()"
      >
        <Paperclip class="icon" />
      </button>
      <input ref="picker" type="file" multiple hidden @change="onPick" />
      <span class="ch-hint" :class="{ over: tooLong || !!failed }">{{
        tooLong
          ? `Message too long (${formatSize(bytes)} of 32 KB)`
          : fileBlock || "Enter to send · Shift+Enter for a new line"
      }}</span>
      <div class="ch-end">
        <Popover v-model:open="menuOpen" up>
          <template #trigger="{ toggle }">
            <Button
              variant="ghost"
              small
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
