<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { BookOpen, FileText, Image as ImageIcon } from "lucide-vue-next";
import type { ChatAttachment } from "../../stores/chats";
import { chatFileUrl, formatSize } from "./chatUtils";
const props = defineProps<{
  a: ChatAttachment;
  chatId: string;
  thumb?: boolean;
}>();
const url = computed(() =>
  props.a.kind !== "kb" && props.a.file_id
    ? chatFileUrl(props.chatId, props.a.file_id)
    : "",
);
</script>
<template>
  <RouterLink
    v-if="a.kind === 'kb'"
    class="ch-chip kb"
    :to="{ path: '/knowledge', query: { doc: a.doc_id } }"
    ><BookOpen class="icon" /><span class="ch-fn">{{
      a.name
    }}</span></RouterLink
  >
  <a
    v-else-if="url && thumb && a.kind === 'image'"
    class="ch-att-img"
    :href="url"
    target="_blank"
    rel="noopener noreferrer"
    :aria-label="`Open ${a.name}`"
    ><img :src="url" :alt="a.name" loading="lazy"
  /></a>
  <component
    :is="url ? 'a' : 'span'"
    v-else
    class="ch-chip"
    :href="url || undefined"
    :target="url ? '_blank' : undefined"
    :rel="url ? 'noopener noreferrer' : undefined"
    ><ImageIcon v-if="a.kind === 'image'" class="icon" /><FileText
      v-else
      class="icon"
    /><span class="ch-fn">{{ a.name }}</span
    ><span class="ch-fs">{{ formatSize(a.size) }}</span></component
  >
</template>
