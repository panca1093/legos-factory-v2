<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import { X } from "lucide-vue-next";
const props = defineProps<{
  open: boolean;
  title: string;
  subtitle?: string;
  drawer?: boolean;
}>();
const emit = defineEmits<{ close: [] }>();
const panel = ref<HTMLElement>();
let previous: HTMLElement | null = null;
let oldOverflow = "";
let background: HTMLElement | null = null;
let wasInert = false;
const controls = () =>
  [
    ...(panel.value?.querySelectorAll<HTMLElement>(
      'button,input,select,textarea,a[href],[tabindex="0"]',
    ) ?? []),
  ].filter((el) => !el.matches(":disabled"));
function keydown(e: KeyboardEvent) {
  if (!props.open) return;
  if (e.key === "Escape") {
    e.preventDefault();
    emit("close");
  }
  if (e.key === "Tab") {
    const items = controls();
    const first = items[0];
    const last = items.at(-1);
    if (!items.length) {
      e.preventDefault();
      panel.value?.focus();
    } else if (
      e.shiftKey &&
      (document.activeElement === first ||
        !panel.value?.contains(document.activeElement))
    ) {
      e.preventDefault();
      last?.focus();
    } else if (
      !e.shiftKey &&
      (document.activeElement === last ||
        !panel.value?.contains(document.activeElement))
    ) {
      e.preventDefault();
      first?.focus();
    }
  }
}
function release() {
  document.removeEventListener("keydown", keydown);
  if (background) {
    background.inert = wasInert;
    background = null;
  }
  document.body.style.overflow = oldOverflow;
  previous?.focus();
}
watch(
  () => props.open,
  async (open) => {
    if (!open) {
      release();
      return;
    }
    previous = document.activeElement as HTMLElement;
    oldOverflow = document.body.style.overflow;
    background = document.querySelector("#app");
    if (background) {
      wasInert = background.inert;
      background.inert = true;
    }
    document.body.style.overflow = "hidden";
    document.addEventListener("keydown", keydown);
    await nextTick();
    controls()[0]?.focus();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  if (props.open) release();
});
</script>
<template>
  <Teleport to="body"
    ><Transition name="modal"
      ><div
        v-if="open"
        class="modal-backdrop"
        :class="{ 'drawer-backdrop': drawer }"
        @click.self="emit('close')"
      >
        <section
          ref="panel"
          class="modal"
          :class="{ drawer }"
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          tabindex="-1"
        >
          <header class="modal-head">
            <div class="row between">
              <div>
                <h2>{{ title }}</h2>
                <p v-if="subtitle" class="help" style="margin-top: 6px">
                  {{ subtitle }}
                </p>
              </div>
              <button
                class="icon-btn"
                aria-label="Close dialog"
                @click="emit('close')"
              >
                <X class="icon" />
              </button>
            </div>
            <slot name="header-extra" />
          </header>
          <div class="modal-body"><slot /></div>
          <footer v-if="$slots.footer" class="modal-footer">
            <slot name="footer" />
          </footer>
        </section></div></Transition
  ></Teleport>
</template>
