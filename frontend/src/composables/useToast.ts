import { ref } from "vue";
export type Toast = { id: number; message: string; undo?: () => void };
const toasts = ref<Toast[]>([]);
let next = 0;
export function useToast() {
  function dismiss(id: number) {
    toasts.value = toasts.value.filter((t) => t.id !== id);
  }
  function toast(message: string, undo?: () => void) {
    const id = ++next;
    toasts.value.push({ id, message, undo });
    setTimeout(() => dismiss(id), undo ? 6000 : 3500);
  }
  return { toasts, toast, dismiss };
}
