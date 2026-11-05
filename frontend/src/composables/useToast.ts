import { ref } from "vue";
export type Toast = { id: number; message: string; undo?: () => void };
const expiry = new Map<number, () => void>();
const toasts = ref<Toast[]>([]);
const timers = new Map<
  number,
  { timer?: ReturnType<typeof setTimeout>; left: number; at: number }
>();
let next = 0;
function dismiss(id: number) {
  expiry.delete(id);
  clearTimeout(timers.get(id)?.timer);
  timers.delete(id);
  toasts.value = toasts.value.filter((t) => t.id !== id);
}
function resume(id: number) {
  const t = timers.get(id);
  if (!t || t.timer) return;
  t.at = Date.now();
  t.timer = setTimeout(() => {
    expiry.get(id)?.();
    dismiss(id);
  }, t.left);
}
function pause(id: number) {
  const t = timers.get(id);
  if (!t?.timer) return;
  clearTimeout(t.timer);
  t.timer = undefined;
  t.left -= Date.now() - t.at;
}
export function useToast() {
  function toast(message: string, undo?: () => void, onExpire?: () => void) {
    const id = ++next;
    if (onExpire) expiry.set(id, onExpire);
    toasts.value.push({ id, message, undo });
    timers.set(id, { left: undo ? 6000 : 3500, at: 0 });
    resume(id);
  }
  return { toasts, toast, dismiss, pause, resume };
}
