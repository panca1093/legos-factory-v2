export function inMonaco(e: Event): boolean {
  return (
    e.target instanceof Element && e.target.closest(".monaco-editor") !== null
  );
}
